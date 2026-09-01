package simulate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"

	"github.com/Caleb-Kelly-25/preflight/internal/engine"
	"github.com/Caleb-Kelly-25/preflight/internal/finding"
)

// fakeIAM records calls and answers from a deny-list. No credentials, no
// network — the whole point of the iamAPI seam.
type fakeIAM struct {
	mu sync.Mutex

	deny       map[string]bool
	missingCtx map[string][]string
	errs       []error // returned in order, one per SimulatePrincipalPolicy call
	contextKey []string
	ctxErr     error

	// pages, when set, makes each call return results in this many chunks.
	pages int
	// staleMarker makes the second page repeat the first page's marker.
	staleMarker bool

	calls []iam.SimulatePrincipalPolicyInput
}

func (f *fakeIAM) SimulatePrincipalPolicy(_ context.Context, in *iam.SimulatePrincipalPolicyInput, _ ...func(*iam.Options)) (*iam.SimulatePrincipalPolicyOutput, error) {
	f.mu.Lock()
	f.calls = append(f.calls, *in)
	n := len(f.calls)
	var err error
	if n <= len(f.errs) {
		err = f.errs[n-1]
	}
	f.mu.Unlock()

	if err != nil {
		return nil, err
	}

	// Reproduce AWS's real two-level response shape, measured 2026-09-01.
	//
	// With one ARN the top-level entry carries the real ARN and the right
	// decision. With several, the top level becomes an AGGREGATE with a
	// TEMPLATED resource name, and the per-ARN answers move into
	// ResourceSpecificResults. A fake that always echoed our ARNs would let a
	// serious bug pass, so it mimics both shapes.
	var results []iamtypes.EvaluationResult
	for _, a := range in.ActionNames {
		d := iamtypes.PolicyEvaluationDecisionTypeAllowed
		if f.deny[a] {
			d = iamtypes.PolicyEvaluationDecisionTypeImplicitDeny
		}

		if len(in.ResourceArns) == 1 {
			results = append(results, iamtypes.EvaluationResult{
				EvalActionName:       aws.String(a),
				EvalResourceName:     aws.String(in.ResourceArns[0]),
				EvalDecision:         d,
				MissingContextValues: f.missingCtx[a],
			})
			continue
		}

		var specific []iamtypes.ResourceSpecificResult
		for _, arn := range in.ResourceArns {
			specific = append(specific, iamtypes.ResourceSpecificResult{
				EvalResourceName:     aws.String(arn),
				EvalResourceDecision: d,
				MissingContextValues: f.missingCtx[a],
			})
		}
		results = append(results, iamtypes.EvaluationResult{
			EvalActionName: aws.String(a),
			// Templated and aggregated, exactly as AWS does it.
			EvalResourceName:        aws.String("arn:aws:s3:::${BucketName}/${KeyName}"),
			EvalDecision:            iamtypes.PolicyEvaluationDecisionTypeImplicitDeny,
			ResourceSpecificResults: specific,
		})
	}

	if f.pages > 1 && in.Marker == nil {
		half := len(results) / 2
		marker := "page2"
		if f.staleMarker {
			marker = "same"
		}
		return &iam.SimulatePrincipalPolicyOutput{
			EvaluationResults: results[:half],
			IsTruncated:       true,
			Marker:            aws.String(marker),
		}, nil
	}
	if f.pages > 1 && in.Marker != nil {
		half := len(results) / 2
		if f.staleMarker {
			// Return the same marker forever; the client must break the loop.
			return &iam.SimulatePrincipalPolicyOutput{
				EvaluationResults: results[:half],
				IsTruncated:       true,
				Marker:            aws.String("same"),
			}, nil
		}
		return &iam.SimulatePrincipalPolicyOutput{EvaluationResults: results[half:]}, nil
	}

	return &iam.SimulatePrincipalPolicyOutput{EvaluationResults: results}, nil
}

func (f *fakeIAM) GetContextKeysForPrincipalPolicy(context.Context, *iam.GetContextKeysForPrincipalPolicyInput, ...func(*iam.Options)) (*iam.GetContextKeysForPrincipalPolicyOutput, error) {
	if f.ctxErr != nil {
		return nil, f.ctxErr
	}
	return &iam.GetContextKeysForPrincipalPolicyOutput{ContextKeyNames: f.contextKey}, nil
}

func (f *fakeIAM) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type fakeSTS struct {
	arn string
	err error
}

func (f *fakeSTS) GetCallerIdentity(context.Context, *sts.GetCallerIdentityInput, ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &sts.GetCallerIdentityOutput{Arn: aws.String(f.arn)}, nil
}

func apiErr(code string) error {
	return &smithy.GenericAPIError{Code: code, Message: code}
}

func item(arn string, actions ...string) engine.RequestItem {
	return engine.RequestItem{Actions: actions, ResourceARN: arn}
}

// ---------------------------------------------------------------------------

func TestResolveAnswersEveryQuestion(t *testing.T) {
	f := &fakeIAM{deny: map[string]bool{"s3:DeleteBucket": true}}
	c := newWithAPIs(f, &fakeSTS{}, Options{})

	req := engine.Request{
		PolicySourceARN: "arn:aws:iam::1:role/r",
		Items: []engine.RequestItem{
			item("arn:aws:s3:::a", "s3:CreateBucket"),
			item("arn:aws:s3:::b", "s3:CreateBucket", "s3:DeleteBucket"),
		},
	}
	resp, err := c.Resolve(context.Background(), req)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if len(resp.Outcomes) != len(req.Items) {
		t.Fatalf("Outcomes length %d, want %d — the engine relies on index alignment",
			len(resp.Outcomes), len(req.Items))
	}
	if got := resp.Outcomes[0].Results[0].Decision; got != finding.DecisionAllowed {
		t.Errorf("item 0: %q, want allowed", got)
	}
	if got := resp.Outcomes[1].Results[1].Decision; got != finding.DecisionImplicitDeny {
		t.Errorf("item 1 delete: %q, want implicitDeny", got)
	}
	for i, o := range resp.Outcomes {
		if o.Err != nil {
			t.Errorf("item %d carries an error: %v", i, o.Err)
		}
	}
}

// TestBatchGroupsByActionSet — the whole point of the two-phase interface.
func TestBatchGroupsByActionSet(t *testing.T) {
	f := &fakeIAM{}
	c := newWithAPIs(f, &fakeSTS{}, Options{})

	var items []engine.RequestItem
	for i := 0; i < 25; i++ {
		items = append(items, item(fmt.Sprintf("arn:aws:s3:::bucket-%d", i), "s3:CreateBucket", "s3:PutBucketTagging"))
	}
	if _, err := c.Resolve(context.Background(), engine.Request{PolicySourceARN: "p", Items: items}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if got := f.callCount(); got != 1 {
		t.Errorf("made %d API calls for 25 resources sharing one action set, want 1", got)
	}
}

// TestBatchSplitsByContext pins the correctness fix to DESIGN §9.2. IAM applies
// ContextEntries per CALL, so two resources with different context values must
// not share one — each would be evaluated under the other's conditions.
func TestBatchSplitsByContext(t *testing.T) {
	f := &fakeIAM{}
	c := newWithAPIs(f, &fakeSTS{}, Options{})

	mk := func(arn, env string) engine.RequestItem {
		return engine.RequestItem{
			Actions: []string{"s3:CreateBucket"}, ResourceARN: arn,
			Context: []finding.ContextEntry{{
				Key: "aws:RequestTag/Environment", Type: finding.ContextString, Values: []string{env},
			}},
		}
	}
	req := engine.Request{PolicySourceARN: "p", Items: []engine.RequestItem{
		mk("arn:aws:s3:::prod", "prod"),
		mk("arn:aws:s3:::dev", "dev"),
	}}
	if _, err := c.Resolve(context.Background(), req); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if got := f.callCount(); got != 2 {
		t.Errorf("made %d calls, want 2 — differing context must not share a call", got)
	}
}

// TestWildcardNeverSharesACallWithConcreteARNs pins an undocumented API
// constraint, found on the first live run:
//
//	InvalidInput: you cannot include both * and individual resources in the
//	resource list for a simulation.
//
// Any plan that creates one resource with a literal name and another of the
// same type whose name is unknown until apply hits this — which is most real
// plans — so without the split the batch fails and both findings degrade.
func TestWildcardNeverSharesACallWithConcreteARNs(t *testing.T) {
	f := &fakeIAM{}
	c := newWithAPIs(f, &fakeSTS{}, Options{})

	req := engine.Request{PolicySourceARN: "p", Items: []engine.RequestItem{
		item("arn:aws:s3:::known-name", "s3:CreateBucket"),
		item("*", "s3:CreateBucket"),
	}}
	if _, err := c.Resolve(context.Background(), req); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if f.callCount() != 2 {
		t.Fatalf("made %d calls, want 2 — the wildcard needs its own", f.callCount())
	}
	for i, call := range f.calls {
		var sawWildcard, sawConcrete bool
		for _, a := range call.ResourceArns {
			if a == "*" {
				sawWildcard = true
			} else {
				sawConcrete = true
			}
		}
		if sawWildcard && sawConcrete {
			t.Errorf("call %d mixes * with concrete ARNs: %v", i, call.ResourceArns)
		}
	}
}

func TestBatchChunksByMaxProduct(t *testing.T) {
	f := &fakeIAM{}
	// 2 actions x 3 ARNs per call.
	c := newWithAPIs(f, &fakeSTS{}, Options{MaxProduct: 6})

	var items []engine.RequestItem
	for i := 0; i < 7; i++ {
		items = append(items, item(fmt.Sprintf("arn:aws:s3:::b%d", i), "s3:CreateBucket", "s3:DeleteBucket"))
	}
	if _, err := c.Resolve(context.Background(), engine.Request{PolicySourceARN: "p", Items: items}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	// 7 ARNs at 3 per call = 3 calls.
	if got := f.callCount(); got != 3 {
		t.Errorf("made %d calls for 7 ARNs at 3 per call, want 3", got)
	}
	for i, call := range f.calls {
		if len(call.ResourceArns)*len(call.ActionNames) > 6 {
			t.Errorf("call %d exceeded MaxProduct: %d actions x %d arns",
				i, len(call.ActionNames), len(call.ResourceArns))
		}
	}
}

// TestCacheServesRepeatedPairs — a replacement asks about the same ARN twice,
// and module instances repeat pairs constantly.
func TestCacheServesRepeatedPairs(t *testing.T) {
	f := &fakeIAM{}
	c := newWithAPIs(f, &fakeSTS{}, Options{})

	req := engine.Request{PolicySourceARN: "p", Items: []engine.RequestItem{
		item("arn:aws:iam::1:role/r", "iam:CreateRole"),
		item("arn:aws:iam::1:role/r", "iam:CreateRole"),
	}}
	resp, err := c.Resolve(context.Background(), req)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := f.callCount(); got != 1 {
		t.Errorf("made %d calls for one distinct question, want 1", got)
	}
	for i, o := range resp.Outcomes {
		if len(o.Results) != 1 || o.Results[0].Decision != finding.DecisionAllowed {
			t.Errorf("outcome %d not answered from cache: %+v", i, o.Results)
		}
	}
}

// TestSurplusProductIsCached — the response is a cartesian product, so pairs we
// did not ask about are already paid for.
func TestSurplusProductIsCached(t *testing.T) {
	f := &fakeIAM{}
	c := newWithAPIs(f, &fakeSTS{}, Options{})

	// Two ARNs sharing an action set: the call returns 2x2 = 4 pairs.
	req := engine.Request{PolicySourceARN: "p", Items: []engine.RequestItem{
		item("arn:aws:s3:::a", "s3:CreateBucket", "s3:DeleteBucket"),
		item("arn:aws:s3:::b", "s3:CreateBucket", "s3:DeleteBucket"),
	}}
	if _, err := c.Resolve(context.Background(), req); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := c.cache.len(); got != 4 {
		t.Errorf("cached %d pairs, want the full 4-pair product", got)
	}
}

func TestPagination(t *testing.T) {
	f := &fakeIAM{pages: 2}
	c := newWithAPIs(f, &fakeSTS{}, Options{})

	req := engine.Request{PolicySourceARN: "p", Items: []engine.RequestItem{
		item("arn:aws:s3:::a", "s3:CreateBucket", "s3:DeleteBucket"),
	}}
	resp, err := c.Resolve(context.Background(), req)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resp.Outcomes[0].Err != nil {
		t.Errorf("paginated result reported unevaluated: %v", resp.Outcomes[0].Err)
	}
	if f.callCount() != 2 {
		t.Errorf("made %d calls, want 2 pages", f.callCount())
	}
}

// TestStaleMarkerTerminates — an infinite pagination loop in someone's CI is
// worse than an Unchecked.
func TestStaleMarkerTerminates(t *testing.T) {
	f := &fakeIAM{pages: 2, staleMarker: true}
	c := newWithAPIs(f, &fakeSTS{}, Options{})

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = c.Resolve(context.Background(), engine.Request{
			PolicySourceARN: "p",
			Items:           []engine.RequestItem{item("arn:aws:s3:::a", "s3:CreateBucket")},
		})
	}()
	<-done

	if f.callCount() > 3 {
		t.Errorf("made %d calls against a non-advancing marker; the loop did not break", f.callCount())
	}
}

// TestPartialResultsAreKept — work AWS already did should not be thrown away,
// as long as the gap is reported rather than treated as a pass.
func TestPartialResultsAreKept(t *testing.T) {
	f := &fakeIAM{}
	c := newWithAPIs(f, &fakeSTS{}, Options{MaxProduct: 1})

	// Two separate calls; the second fails.
	f.errs = []error{nil, apiErr("ThrottlingException")}

	req := engine.Request{PolicySourceARN: "p", Items: []engine.RequestItem{
		item("arn:aws:s3:::a", "s3:CreateBucket"),
		item("arn:aws:s3:::b", "s3:CreateBucket"),
	}}
	resp, err := c.Resolve(context.Background(), req)
	if err != nil {
		t.Fatalf("Resolve returned fatal on a throttle: %v", err)
	}

	var answered, unevaluated int
	for _, o := range resp.Outcomes {
		if o.Err != nil {
			unevaluated++
		} else {
			answered++
		}
	}
	if answered != 1 || unevaluated != 1 {
		t.Errorf("answered=%d unevaluated=%d, want 1 and 1", answered, unevaluated)
	}
	if len(resp.Warnings) == 0 {
		t.Error("a throttled batch produced no warning")
	}
}

// TestAccessDeniedIsFatalAndStopsWork — forty more doomed calls waste the
// user's time and AWS's rate limit.
func TestAccessDeniedIsFatalAndStopsWork(t *testing.T) {
	f := &fakeIAM{}
	f.errs = []error{apiErr("AccessDenied")}
	c := newWithAPIs(f, &fakeSTS{}, Options{MaxProduct: 1, StartConcurrency: 1, MaxConcurrency: 1})

	var items []engine.RequestItem
	for i := 0; i < 20; i++ {
		items = append(items, item(fmt.Sprintf("arn:aws:s3:::b%d", i), "s3:CreateBucket"))
	}
	_, err := c.Resolve(context.Background(), engine.Request{PolicySourceARN: "p", Items: items})

	var perm *PermissionError
	if !errors.As(err, &perm) {
		t.Fatalf("err = %v, want *PermissionError", err)
	}
	if got := f.callCount(); got > 5 {
		t.Errorf("made %d calls after AccessDenied; remaining work was not cancelled", got)
	}
}

func TestNoSuchEntityIsFatal(t *testing.T) {
	f := &fakeIAM{}
	f.errs = []error{apiErr("NoSuchEntity")}
	c := newWithAPIs(f, &fakeSTS{}, Options{})

	_, err := c.Resolve(context.Background(), engine.Request{
		PolicySourceARN: "arn:aws:iam::1:role/ghost",
		Items:           []engine.RequestItem{item("arn:aws:s3:::a", "s3:CreateBucket")},
	})

	var missing *PrincipalNotFoundError
	if !errors.As(err, &missing) {
		t.Fatalf("err = %v, want *PrincipalNotFoundError", err)
	}
}

func TestInvalidInputDegradesBatchOnly(t *testing.T) {
	f := &fakeIAM{}
	f.errs = []error{apiErr("InvalidInput"), nil}
	c := newWithAPIs(f, &fakeSTS{}, Options{MaxProduct: 1, StartConcurrency: 1, MaxConcurrency: 1})

	resp, err := c.Resolve(context.Background(), engine.Request{PolicySourceARN: "p", Items: []engine.RequestItem{
		item("arn:aws:s3:::bad", "s3:CreateBucket"),
		item("arn:aws:s3:::good", "s3:CreateBucket"),
	}})
	if err != nil {
		t.Fatalf("InvalidInput killed the whole run: %v", err)
	}
	if resp.Outcomes[0].Err == nil {
		t.Error("the malformed batch was not marked unevaluated")
	}
	if resp.Outcomes[1].Err != nil {
		t.Error("a good batch was degraded by an unrelated InvalidInput")
	}
	if len(resp.Warnings) == 0 {
		t.Error("InvalidInput produced no warning; a bad synthesised ARN must be loud")
	}
}

func TestContextEntriesReachTheAPI(t *testing.T) {
	f := &fakeIAM{}
	c := newWithAPIs(f, &fakeSTS{}, Options{})

	_, err := c.Resolve(context.Background(), engine.Request{PolicySourceARN: "p", Items: []engine.RequestItem{{
		Actions: []string{"s3:CreateBucket"}, ResourceARN: "arn:aws:s3:::a",
		Context: []finding.ContextEntry{{
			Key: "aws:RequestedRegion", Type: finding.ContextString, Values: []string{"eu-west-1"},
		}},
	}}})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	entries := f.calls[0].ContextEntries
	if len(entries) != 1 || aws.ToString(entries[0].ContextKeyName) != "aws:RequestedRegion" {
		t.Fatalf("ContextEntries = %+v", entries)
	}
	if entries[0].ContextKeyValues[0] != "eu-west-1" {
		t.Errorf("value = %v", entries[0].ContextKeyValues)
	}
	if string(entries[0].ContextKeyType) != "string" {
		t.Errorf("type = %q", entries[0].ContextKeyType)
	}
}

func TestMissingContextValuesPropagate(t *testing.T) {
	f := &fakeIAM{
		deny:       map[string]bool{"s3:CreateBucket": true},
		missingCtx: map[string][]string{"s3:CreateBucket": {"aws:RequestTag/Env"}},
	}
	c := newWithAPIs(f, &fakeSTS{}, Options{})

	resp, err := c.Resolve(context.Background(), engine.Request{PolicySourceARN: "p",
		Items: []engine.RequestItem{item("arn:aws:s3:::a", "s3:CreateBucket")}})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	got := resp.Outcomes[0].Results[0].MissingContextValues
	if len(got) != 1 || got[0] != "aws:RequestTag/Env" {
		t.Errorf("MissingContextValues = %v", got)
	}
}

// TestUnmatchedResourceNameIsNotGuessed — R1 in the plan's risk list. If AWS
// ever normalised the ARN it echoes back, guessing would silently attach the
// wrong decision to the wrong resource.
func TestUnmatchedResourceNameIsNotGuessed(t *testing.T) {
	b := batch{arns: []string{"arn:aws:s3:::a", "arn:aws:s3:::b"}}

	if _, ok := matchARN(aws.String("arn:aws:s3:::completely-different"), b.arns); ok {
		t.Error("matched an ARN we never sent")
	}
	if got, ok := matchARN(aws.String("ARN:AWS:S3:::A"), b.arns); !ok || got != "arn:aws:s3:::a" {
		t.Errorf("case-insensitive fallback failed: %q %v", got, ok)
	}
	if got, ok := matchARN(nil, []string{"only"}); !ok || got != "only" {
		t.Error("a nil resource name with one ARN in the call is unambiguous")
	}
	if _, ok := matchARN(nil, b.arns); ok {
		t.Error("a nil resource name with two ARNs must not be guessed")
	}
}

// TestMultiARNUsesResourceSpecificResults pins the finding that a multi-ARN
// call answers in a different place from a single-ARN one.
//
// The top-level EvalDecision for a multi-ARN call is an aggregate over all of
// them, so reading it would report an allowed resource as denied — a false
// positive on every batched call, which is the failure mode that trains teams
// to bypass the tool.
func TestMultiARNUsesResourceSpecificResults(t *testing.T) {
	f := &fakeIAM{}
	c := newWithAPIs(f, &fakeSTS{}, Options{})

	req := engine.Request{PolicySourceARN: "p", Items: []engine.RequestItem{
		item("arn:aws:s3:::a", "s3:CreateBucket"),
		item("arn:aws:s3:::b", "s3:CreateBucket"),
	}}
	resp, err := c.Resolve(context.Background(), req)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if f.callCount() != 1 {
		t.Fatalf("made %d calls; this test needs the two ARNs batched together", f.callCount())
	}
	for i, o := range resp.Outcomes {
		if o.Err != nil {
			t.Fatalf("item %d unevaluated — per-resource results were not read: %v", i, o.Err)
		}
		// The fake sets the top-level aggregate to implicitDeny while every
		// per-resource decision is allowed. Reading the wrong level shows up
		// here as a spurious denial.
		if got := o.Results[0].Decision; got != finding.DecisionAllowed {
			t.Errorf("item %d decision = %q, want allowed — the aggregate was read instead of the per-resource result", i, got)
		}
	}
}

func TestStatsRecorded(t *testing.T) {
	f := &fakeIAM{}
	c := newWithAPIs(f, &fakeSTS{}, Options{})

	resp, err := c.Resolve(context.Background(), engine.Request{PolicySourceARN: "p",
		Items: []engine.RequestItem{item("arn:aws:s3:::a", "s3:CreateBucket", "s3:DeleteBucket")}})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resp.Stats.Calls != 1 || resp.Stats.Evaluations != 2 {
		t.Errorf("Stats = %+v, want 1 call and 2 evaluations", resp.Stats)
	}
	// Deliberately no assertion on Elapsed: against a fake the whole run
	// finishes inside one clock tick, so any lower bound tests the platform's
	// timer resolution rather than our code.
	if resp.Stats.Elapsed < 0 {
		t.Errorf("Elapsed = %v", resp.Stats.Elapsed)
	}
}

// TestCacheHitsCounted keeps the batching-effectiveness signal honest: the
// Calls-to-Evaluations ratio is how we detect context fragmentation in the
// field, so the inputs to it have to be right.
func TestCacheHitsCounted(t *testing.T) {
	f := &fakeIAM{}
	c := newWithAPIs(f, &fakeSTS{}, Options{})

	req := engine.Request{PolicySourceARN: "p", Items: []engine.RequestItem{
		item("arn:aws:s3:::a", "s3:CreateBucket"),
		item("arn:aws:s3:::a", "s3:CreateBucket"),
		item("arn:aws:s3:::a", "s3:CreateBucket"),
	}}
	resp, err := c.Resolve(context.Background(), req)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if f.callCount() != 1 {
		t.Errorf("made %d calls for one distinct question", f.callCount())
	}
	if resp.Stats.Evaluations != 1 {
		t.Errorf("Evaluations = %d, want 1", resp.Stats.Evaluations)
	}
}

// ---------------------------------------------------------------------------
// Identity and permissions
// ---------------------------------------------------------------------------

func TestResolveIdentityTranslatesSessionARN(t *testing.T) {
	id, err := ResolveIdentity(context.Background(),
		&fakeSTS{arn: "arn:aws:sts::123456789012:assumed-role/deploy-role/GitHubActions"})
	if err != nil {
		t.Fatalf("ResolveIdentity: %v", err)
	}
	if want := "arn:aws:iam::123456789012:role/deploy-role"; id.PolicySourceARN != want {
		t.Errorf("PolicySourceARN = %q, want %q", id.PolicySourceARN, want)
	}
	if !id.Simulatable() {
		t.Error("resolved identity is not simulatable")
	}
}

func TestResolveIdentityNoCredentials(t *testing.T) {
	_, err := ResolveIdentity(context.Background(),
		&fakeSTS{err: errors.New("failed to refresh cached credentials, no EC2 IMDS role found")})
	if !errors.Is(err, ErrNoCredentials) {
		t.Errorf("err = %v, want ErrNoCredentials", err)
	}
}

func TestResolveIdentityAccessDenied(t *testing.T) {
	_, err := ResolveIdentity(context.Background(), &fakeSTS{err: apiErr("AccessDenied")})
	var perm *PermissionError
	if !errors.As(err, &perm) {
		t.Errorf("err = %v, want *PermissionError", err)
	}
}

func TestReferencedContextKeys(t *testing.T) {
	keys, err := ReferencedContextKeys(context.Background(),
		&fakeIAM{contextKey: []string{"aws:RequestedRegion"}}, "arn:aws:iam::1:role/r")
	if err != nil {
		t.Fatalf("ReferencedContextKeys: %v", err)
	}
	if len(keys) != 1 || keys[0] != "aws:RequestedRegion" {
		t.Errorf("keys = %v", keys)
	}
}

func TestReferencedContextKeysAccessDeniedIsFatal(t *testing.T) {
	// Per the design decision: this probe is required, because it is the only
	// signal that a condition key is in play at all.
	_, err := ReferencedContextKeys(context.Background(),
		&fakeIAM{ctxErr: apiErr("AccessDenied")}, "arn:aws:iam::1:role/r")
	var perm *PermissionError
	if !errors.As(err, &perm) {
		t.Fatalf("err = %v, want *PermissionError", err)
	}
	if perm.Action != "iam:GetContextKeysForPrincipalPolicy" {
		t.Errorf("Action = %q", perm.Action)
	}
}

func TestRequiredPolicyJSON(t *testing.T) {
	out := RequiredPolicyJSON("arn:aws:iam::1:role/deploy")

	var doc struct {
		Statement []struct {
			Action   any `json:"Action"`
			Resource any `json:"Resource"`
		} `json:"Statement"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, out)
	}
	if len(doc.Statement) != 2 {
		t.Fatalf("want 2 statements, got %d", len(doc.Statement))
	}

	// The IAM actions must be scoped to the principal, not "*".
	// SimulatePrincipalPolicy discloses whatever principal it is pointed at, so
	// a wildcard grant hands the holder a way to enumerate the whole account.
	if got, ok := doc.Statement[0].Resource.(string); !ok || got != "arn:aws:iam::1:role/deploy" {
		t.Errorf("simulate statement Resource = %v, want the principal ARN", doc.Statement[0].Resource)
	}
	for _, a := range RequiredActions {
		if !strings.Contains(out, a) {
			t.Errorf("policy omits %s", a)
		}
	}
}

func TestClassifyErr(t *testing.T) {
	for code, want := range map[string]errKind{
		"AccessDenied":         errFatalPermission,
		"NoSuchEntity":         errFatalPrincipal,
		"ThrottlingException":  errBatchThrottled,
		"RequestLimitExceeded": errBatchThrottled,
		"InvalidInput":         errBatchInvalidInput,
		"ServiceFailure":       errBatchTransient,
	} {
		if got := classifyErr(apiErr(code)); got != want {
			t.Errorf("classifyErr(%s) = %v, want %v", code, got, want)
		}
	}
	if got := classifyErr(context.DeadlineExceeded); got != errBatchTransient {
		t.Errorf("a timeout should degrade a batch, not the run: %v", got)
	}
}
