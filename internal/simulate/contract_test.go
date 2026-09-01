//go:build awsintegration

// Contract tests against real AWS.
//
// These exist to turn the M1 spike's one-off measurements into standing
// regression detectors on AWS's own behaviour. Every assertion here encodes a
// property the classifier depends on; if AWS changes one, a test fails and we
// learn from CI rather than from a user reporting a wrong answer.
//
// Guarded twice on purpose: the build tag AND an environment variable. A
// build-tag typo in a CI matrix must not be able to reach AWS.
//
// Run with:
//
//	PREFLIGHT_CONTRACT_ACCOUNT=<your-account-id> make contract
//
// Fixtures are provisioned by testfixtures/aws.
package simulate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"

	"github.com/Caleb-Kelly-25/preflight/internal/engine"
	"github.com/Caleb-Kelly-25/preflight/internal/finding"
)

const fixturePrefix = "preflight-contract"

func contractAccount(t *testing.T) string {
	t.Helper()
	acct := os.Getenv("PREFLIGHT_CONTRACT_ACCOUNT")
	if acct == "" {
		t.Skip("PREFLIGHT_CONTRACT_ACCOUNT not set; skipping contract tests")
	}
	return acct
}

func contractClient(t *testing.T) (*Client, string) {
	t.Helper()
	acct := contractAccount(t)
	cfg, err := LoadConfig(context.Background(), "us-east-1", 0)
	if err != nil {
		t.Fatalf("loading AWS config: %v", err)
	}
	return New(cfg, Options{}), acct
}

func roleARN(acct, suffix string) string {
	return fmt.Sprintf("arn:aws:iam::%s:role/%s-%s", acct, fixturePrefix, suffix)
}

func bucketARN(name string) string {
	return fmt.Sprintf("arn:aws:s3:::%s-%s", fixturePrefix, name)
}

// simulate is a thin helper returning the decision for one action against one
// ARN, going through the real client so the tests exercise the code we ship.
func decide(t *testing.T, c *Client, principal, action, arn string, ctx []finding.ContextEntry) finding.ActionResult {
	t.Helper()
	resp, err := c.Resolve(context.Background(), engine.Request{
		PolicySourceARN: principal,
		Items: []engine.RequestItem{{
			Actions: []string{action}, ResourceARN: arn, Context: ctx,
		}},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(resp.Outcomes) != 1 || len(resp.Outcomes[0].Results) != 1 {
		t.Fatalf("unexpected response shape: %+v", resp.Outcomes)
	}
	return resp.Outcomes[0].Results[0]
}

// TestContractExactARNAllowed is the baseline. If this fails, nothing else in
// this file means anything.
func TestContractExactARNAllowed(t *testing.T) {
	c, acct := contractClient(t)
	got := decide(t, c, roleARN(acct, "exact-arn"), "s3:CreateBucket", bucketARN("exact-bucket"), nil)
	if got.Decision != finding.DecisionAllowed {
		t.Errorf("decision = %q, want allowed", got.Decision)
	}
}

// TestContractWildcardDeniesScopedPolicy pins E1 and E5 — the measurement the
// entire wildcard rule rests on. If AWS ever made "*" match an ARN-scoped
// policy, a wildcard allow would become a false negative and the classifier
// would need to stop trusting it.
func TestContractWildcardDeniesScopedPolicy(t *testing.T) {
	c, acct := contractClient(t)

	for _, fixture := range []string{"exact-arn", "prefix-arn"} {
		t.Run(fixture, func(t *testing.T) {
			got := decide(t, c, roleARN(acct, fixture), "s3:CreateBucket", "*", nil)
			if !got.Decision.Denied() {
				t.Errorf("decision = %q, want a denial.\n"+
					"ResourceArns \"*\" now matches an ARN-scoped policy. A wildcard allow is no "+
					"longer trustworthy, and internal/engine/classify.go must stop treating it as "+
					"informative. See docs/DESIGN.md §6.3.", got.Decision)
			}
		})
	}
}

// TestContractPartialWildcardIsLiteral pins E3: ResourceArns values are literal
// resource identifiers, not patterns. This is why the ARN prefix strategy has to
// synthesise a representative concrete ARN rather than a glob.
func TestContractPartialWildcardIsLiteral(t *testing.T) {
	c, acct := contractClient(t)
	got := decide(t, c, roleARN(acct, "exact-arn"), "s3:CreateBucket", bucketARN("*"), nil)
	if !got.Decision.Denied() {
		t.Errorf("decision = %q, want a denial.\n"+
			"A partial wildcard now behaves as a pattern rather than a literal name. The prefix "+
			"strategy in docs/DESIGN.md §6.1 could be simplified.", got.Decision)
	}
}

// TestContractPrefixPolicyMatchesConcreteARN pins E4, which is the ground truth
// the M3 representative-concrete-ARN strategy will build on.
func TestContractPrefixPolicyMatchesConcreteARN(t *testing.T) {
	c, acct := contractClient(t)
	got := decide(t, c, roleARN(acct, "prefix-arn"), "s3:CreateBucket", bucketARN("anything-at-all"), nil)
	if got.Decision != finding.DecisionAllowed {
		t.Errorf("decision = %q, want allowed", got.Decision)
	}
}

// TestContractRequestedRegionTrapStillExists is the most valuable test here.
//
// It asserts that the silent-allow trap is still real: a policy gated on
// aws:RequestedRegion, with no value supplied, returns ALLOWED and reports
// NOTHING missing. If AWS ever fixes this, this test fails — and that is good
// news we would otherwise never notice, letting us relax a downgrade.
func TestContractRequestedRegionTrapStillExists(t *testing.T) {
	c, acct := contractClient(t)
	got := decide(t, c, roleARN(acct, "condition-key"), "s3:CreateBucket", "*", nil)

	if got.Decision != finding.DecisionAllowed {
		t.Errorf("decision = %q, want allowed — the trap is that this succeeds", got.Decision)
	}
	if len(got.MissingContextValues) != 0 {
		t.Errorf("MissingContextValues = %v, want empty.\n"+
			"AWS now reports the unsupplied key. MissingContextValues may have become "+
			"trustworthy on the allow path, which would let internal/engine relax the "+
			"unconditional downgrade in docs/DESIGN.md §7.3.", got.MissingContextValues)
	}
}

// TestContractSuppliedRegionEvaluates and its sibling pin R4 and C3: a supplied
// value is genuinely evaluated, in both directions.
func TestContractSuppliedRegionEvaluates(t *testing.T) {
	c, acct := contractClient(t)
	principal := roleARN(acct, "condition-key")

	right := decide(t, c, principal, "s3:CreateBucket", "*", []finding.ContextEntry{{
		Key: "aws:RequestedRegion", Type: finding.ContextString, Values: []string{"us-east-1"},
	}})
	if right.Decision != finding.DecisionAllowed {
		t.Errorf("matching region: decision = %q, want allowed", right.Decision)
	}

	wrong := decide(t, c, principal, "s3:CreateBucket", "*", []finding.ContextEntry{{
		Key: "aws:RequestedRegion", Type: finding.ContextString, Values: []string{"eu-west-1"},
	}})
	if !wrong.Decision.Denied() {
		t.Errorf("mismatched region: decision = %q, want a denial", wrong.Decision)
	}
}

// TestContractPathlessRoleResolves pins E8c: role names are unique account-wide,
// so a pathless ARN resolves a role that has a path. This is why preflight needs
// no iam:GetRole call and carries no path caveat.
func TestContractPathlessRoleResolves(t *testing.T) {
	c, acct := contractClient(t)
	pathless := fmt.Sprintf("arn:aws:iam::%s:role/%s-pathed", acct, fixturePrefix)

	got := decide(t, c, pathless, "s3:CreateBucket", bucketARN("exact-bucket"), nil)
	if got.Decision != finding.DecisionAllowed {
		t.Errorf("decision = %q, want allowed.\n"+
			"A pathless ARN no longer resolves a role with an IAM path. preflight would need "+
			"an iam:GetRole call and a path caveat again. See docs/DESIGN.md §5.1.", got.Decision)
	}
}

// TestContractNonexistentPrincipalErrors pins E8d: AWS fails loudly rather than
// returning a misleading denial, which is why there is no silent-failure mode to
// defend against.
func TestContractNonexistentPrincipalErrors(t *testing.T) {
	c, acct := contractClient(t)
	ghost := fmt.Sprintf("arn:aws:iam::%s:role/%s-definitely-not-a-real-role", acct, fixturePrefix)

	_, err := c.Resolve(context.Background(), engine.Request{
		PolicySourceARN: ghost,
		Items:           []engine.RequestItem{{Actions: []string{"s3:CreateBucket"}, ResourceARN: "*"}},
	})

	var missing *PrincipalNotFoundError
	if !errors.As(err, &missing) {
		t.Errorf("err = %v, want *PrincipalNotFoundError.\n"+
			"A nonexistent principal no longer errors. If it now returns a denial instead, "+
			"that denial is meaningless and preflight would be reporting phantom gaps.", err)
	}
}

// TestContractMultiARNUsesResourceSpecificResults pins the finding that cost the
// most to discover: with several ARNs in one call, the top-level EvalDecision is
// an AGGREGATE over all of them and EvalResourceName is a TEMPLATE. The real
// per-ARN answers live in ResourceSpecificResults.
//
// Reading the wrong level reports an allowed resource as denied on every batched
// call, so this asserts against the raw API rather than through the client.
func TestContractMultiARNUsesResourceSpecificResults(t *testing.T) {
	_, acct := contractClient(t)
	cfg, err := LoadConfig(context.Background(), "us-east-1", 0)
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}
	api := iam.NewFromConfig(cfg)

	allowed := bucketARN("exact-bucket")
	denied := bucketARN("some-other-bucket")

	out, err := api.SimulatePrincipalPolicy(context.Background(), &iam.SimulatePrincipalPolicyInput{
		PolicySourceArn: aws.String(roleARN(acct, "exact-arn")),
		ActionNames:     []string{"s3:CreateBucket"},
		ResourceArns:    []string{allowed, denied},
	})
	if err != nil {
		t.Fatalf("SimulatePrincipalPolicy: %v", err)
	}
	if len(out.EvaluationResults) != 1 {
		t.Fatalf("got %d top-level results, want 1 aggregate", len(out.EvaluationResults))
	}

	rsr := out.EvaluationResults[0].ResourceSpecificResults
	if len(rsr) != 2 {
		t.Fatalf("ResourceSpecificResults has %d entries, want 2.\n"+
			"Per-ARN answers have moved. internal/simulate/paginate.go reads them from here; "+
			"if they are no longer present it will attribute an aggregate decision to every "+
			"resource in the batch.", len(rsr))
	}

	byARN := map[string]iamtypes.PolicyEvaluationDecisionType{}
	for _, r := range rsr {
		byARN[aws.ToString(r.EvalResourceName)] = r.EvalResourceDecision
	}
	if byARN[allowed] != iamtypes.PolicyEvaluationDecisionTypeAllowed {
		t.Errorf("%s = %q, want allowed", allowed, byARN[allowed])
	}
	if byARN[denied] == iamtypes.PolicyEvaluationDecisionTypeAllowed {
		t.Errorf("%s = allowed, want a denial", denied)
	}
}

// TestContractWildcardCannotShareACallWithConcreteARNs pins the undocumented
// constraint found on the first live run. Without the split in batch.go, any
// plan mixing a known-name resource with an unknown-name one of the same type
// fails its whole batch.
func TestContractWildcardCannotShareACallWithConcreteARNs(t *testing.T) {
	_, acct := contractClient(t)
	cfg, err := LoadConfig(context.Background(), "us-east-1", 0)
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}
	api := iam.NewFromConfig(cfg)

	_, err = api.SimulatePrincipalPolicy(context.Background(), &iam.SimulatePrincipalPolicyInput{
		PolicySourceArn: aws.String(roleARN(acct, "exact-arn")),
		ActionNames:     []string{"s3:CreateBucket"},
		ResourceArns:    []string{"*", bucketARN("exact-bucket")},
	})
	if err == nil {
		t.Error("mixing * with a concrete ARN was accepted.\n" +
			"internal/simulate/batch.go splits these into separate calls to avoid a rejection. " +
			"If AWS now allows it, that split could be removed for better batching.")
		return
	}
	if classifyErr(err) != errBatchInvalidInput {
		t.Errorf("err = %v, classified as %v; expected InvalidInput", err, classifyErr(err))
	}
}

// TestContractStatsAndBatching exercises a realistic batch and logs Stats, which
// is how docs/DESIGN.md open question 4 (real IAM throttling limits) gets its
// number from the field rather than a guess.
func TestContractStatsAndBatching(t *testing.T) {
	c, acct := contractClient(t)

	var items []engine.RequestItem
	for i := 0; i < 40; i++ {
		items = append(items, engine.RequestItem{
			Actions:     []string{"s3:CreateBucket", "s3:DeleteBucket"},
			ResourceARN: bucketARN(fmt.Sprintf("batch-%d", i)),
		})
	}

	resp, err := c.Resolve(context.Background(), engine.Request{
		PolicySourceARN: roleARN(acct, "prefix-arn"),
		Items:           items,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	for i, o := range resp.Outcomes {
		if o.Err != nil {
			t.Errorf("item %d unevaluated: %v", i, o.Err)
		}
	}
	t.Logf("40 resources x 2 actions -> calls=%d pages=%d evaluations=%d throttles=%d elapsed=%v",
		resp.Stats.Calls, resp.Stats.Pages, resp.Stats.Evaluations, resp.Stats.Throttles, resp.Stats.Elapsed)

	if resp.Stats.Calls > 4 {
		t.Errorf("made %d calls for one action set; batching is not grouping as intended", resp.Stats.Calls)
	}
}

// TestContractContextKeysReported confirms the probe preflight now requires
// actually reports what its policy references.
func TestContractContextKeysReported(t *testing.T) {
	c, acct := contractClient(t)

	keys, err := ReferencedContextKeys(context.Background(), c.IAM(), roleARN(acct, "condition-key"))
	if err != nil {
		t.Fatalf("ReferencedContextKeys: %v", err)
	}
	var found bool
	for _, k := range keys {
		if k == "aws:RequestedRegion" {
			found = true
		}
	}
	if !found {
		t.Errorf("keys = %v, want aws:RequestedRegion.\n"+
			"This probe is the only reliable signal that a condition key is in play; if it "+
			"stops reporting them, the silent-allow trap becomes undetectable.", keys)
	}
}
