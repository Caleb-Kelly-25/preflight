package engine_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Caleb-Kelly-25/preflight/internal/engine"
	"github.com/Caleb-Kelly-25/preflight/internal/finding"
	"github.com/Caleb-Kelly-25/preflight/internal/mapping"
	"github.com/Caleb-Kelly-25/preflight/internal/plan"
	"github.com/Caleb-Kelly-25/preflight/internal/principal"
)

// fakeSimulator answers from fixed tables, so classification can be exercised
// without touching AWS.
type fakeSimulator struct {
	deny       map[string]bool
	missingCtx map[string][]string // action -> MissingContextValues
	err        error               // whole-call failure
	failItems  map[int]bool        // per-item batch failure
	dropLast   bool                // return a short Outcomes slice

	requests []engine.Request // recorded for assertions
}

func (f *fakeSimulator) Resolve(_ context.Context, req engine.Request) (engine.Response, error) {
	f.requests = append(f.requests, req)
	if f.err != nil {
		return engine.Response{}, f.err
	}

	var resp engine.Response
	for i, item := range req.Items {
		if f.failItems[i] {
			resp.Outcomes = append(resp.Outcomes, engine.ItemOutcome{
				Err: fmt.Errorf("batch throttled past retries"),
			})
			continue
		}
		results := make([]finding.ActionResult, 0, len(item.Actions))
		for _, a := range item.Actions {
			d := finding.DecisionAllowed
			if f.deny[a] {
				d = finding.DecisionImplicitDeny
			}
			results = append(results, finding.ActionResult{
				Action:               a,
				ResourceARN:          item.ResourceARN,
				Decision:             d,
				MissingContextValues: f.missingCtx[a],
			})
		}
		resp.Outcomes = append(resp.Outcomes, engine.ItemOutcome{Results: results})
	}
	if f.dropLast && len(resp.Outcomes) > 0 {
		resp.Outcomes = resp.Outcomes[:len(resp.Outcomes)-1]
	}
	return resp, nil
}

// testDB builds a mapping database in-test rather than using the shipped one.
// Classification must be provable independent of whatever the mapping files
// contain today — and every shipped entry is draft, which would make Verified
// unreachable in every test.
//
// aws_s3_bucket is resource-policy-capable and in an RCP-governed service, so
// it can never reach Verified. aws_iam_role is neither, so it can.
func testDB(t *testing.T, iamRoleStatus string) *mapping.Database {
	t.Helper()
	doc := fmt.Sprintf(`
resources:
  - type: aws_s3_bucket
    service: s3
    status: verified
    arn_format: "arn:${Partition}:s3:::${BucketName}"
    arn_attributes: { BucketName: bucket }
    resource_policy_capable: true
    context_keys:
      "aws:RequestTag/*": { from: tags }
    operations:
      create: [s3:CreateBucket]
      update: [s3:PutBucketTagging]
      delete: [s3:DeleteBucket]
  - type: aws_iam_role
    service: iam
    status: %s
    arn_format: "arn:${Partition}:iam::${Account}:role/${RoleName}"
    arn_attributes: { RoleName: name }
    operations:
      create: [iam:CreateRole]
      delete: [iam:DeleteRole]
`, iamRoleStatus)

	db, err := mapping.Load(fstest.MapFS{"test.yaml": &fstest.MapFile{Data: []byte(doc)}})
	if err != nil {
		t.Fatalf("building test mapping database: %v", err)
	}
	return db
}

func testPlan(t *testing.T) *plan.Plan {
	t.Helper()
	f, err := os.Open("../plan/testdata/simple.tfplan.json")
	if err != nil {
		t.Fatalf("opening fixture: %v", err)
	}
	defer f.Close()
	p, err := plan.Parse(f)
	if err != nil {
		t.Fatalf("parsing fixture: %v", err)
	}
	return p
}

// iamUser is a principal whose ARN is exactly known.
func iamUser(t *testing.T) principal.Identity {
	t.Helper()
	id, err := principal.Resolve("arn:aws:iam::123456789012:user/deployer")
	if err != nil {
		t.Fatalf("resolving principal: %v", err)
	}
	return id
}

// analyze runs the engine over the shared fixture, filling in the defaults that
// most tests do not care about.
func analyze(t *testing.T, opts engine.Options) *finding.Report {
	t.Helper()
	if opts.Identity.PolicySourceARN == "" {
		opts.Identity = iamUser(t)
	}
	if opts.Region == "" {
		opts.Region = "us-east-1"
	}
	rep, err := engine.Analyze(context.Background(), testPlan(t), opts)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	return rep
}

func findingFor(t *testing.T, r *finding.Report, addr, op string) finding.Finding {
	t.Helper()
	for _, f := range r.Findings {
		if f.ResourceAddress == addr && f.Operation == op {
			return f
		}
	}
	t.Fatalf("no finding for %s %s", addr, op)
	return finding.Finding{}
}

func hasReason(f finding.Finding, r finding.Reason) bool {
	for _, got := range f.Reasons {
		if got == r {
			return true
		}
	}
	return false
}

func requireReason(t *testing.T, f finding.Finding, r finding.Reason) {
	t.Helper()
	if !hasReason(f, r) {
		t.Errorf("%s %s: missing reason %q, got %v", f.ResourceAddress, f.Operation, r, f.Reasons)
	}
}

func requireLevel(t *testing.T, f finding.Finding, want finding.Level) {
	t.Helper()
	if f.Level != want {
		t.Errorf("%s %s: Level = %q, want %q (reasons: %v)",
			f.ResourceAddress, f.Operation, f.Level, want, f.Reasons)
	}
}

// ---------------------------------------------------------------------------
// The defect this milestone exists to fix
// ---------------------------------------------------------------------------

// TestWildcardDenialIsNotAMissingPermission pins the bug that prompted the
// redesign.
//
// Measured (E1, E5): ResourceArns "*" does not match a policy scoped to
// specific ARNs, so a wildcard denial proves only that the policy is scoped —
// not that the real ARN falls outside it. Before this change the engine
// downgraded the LEVEL but left the denied ActionResult in place, so Denied(),
// Counts(), ShouldFail() and the text report all still treated it as a
// confirmed missing permission: exit 1 and "This plan will fail at apply time",
// for a plan that may well be fine.
func TestWildcardDenialIsNotAMissingPermission(t *testing.T) {
	rep := analyze(t, engine.Options{
		Database:  testDB(t, "verified"),
		Simulator: &fakeSimulator{deny: map[string]bool{"s3:CreateBucket": true}},
	})

	// artifacts has an unknown bucket name, so it was simulated against "*".
	f := findingFor(t, rep, "aws_s3_bucket.artifacts", "create")

	if f.Denied() {
		t.Error("wildcard denial reported as a missing permission")
	}
	if got := f.MissingActions(); len(got) != 0 {
		t.Errorf("MissingActions() = %v, want empty", got)
	}
	requireLevel(t, f, finding.LevelUnchecked)
	requireReason(t, f, finding.ReasonARNUnresolved)

	// Nothing is hidden: the raw denial is still visible for --explain.
	sup := f.SuppressedDenials()
	if len(sup) != 1 || sup[0].Action != "s3:CreateBucket" {
		t.Fatalf("SuppressedDenials() = %+v, want the raw s3:CreateBucket denial", sup)
	}
	if sup[0].Decision != finding.DecisionImplicitDeny {
		t.Errorf("raw decision was lost: %q", sup[0].Decision)
	}
	if sup[0].InconclusiveReason != finding.ReasonARNUnresolved {
		t.Errorf("InconclusiveReason = %q, want %q", sup[0].InconclusiveReason, finding.ReasonARNUnresolved)
	}

	// And it must not fail CI. This is the assertion the old test was missing.
	//
	// Asserted against a plan containing ONLY the unknown-name bucket: the
	// shared fixture also creates a bucket whose name is literal, and that one's
	// denial is conclusive and should quite correctly fail the run.
	isolated := &plan.Plan{
		FormatVersion: "1.2",
		ResourceChanges: []plan.ResourceChange{{
			Address: "aws_s3_bucket.artifacts", Mode: "managed", Type: "aws_s3_bucket",
			Name: "artifacts", ProviderName: "registry.terraform.io/hashicorp/aws",
			Change: plan.Change{
				Actions:      []plan.Action{plan.ActionCreate},
				After:        map[string]any{"force_destroy": false},
				AfterUnknown: map[string]any{"bucket": true},
			},
		}},
	}
	only, err := engine.Analyze(context.Background(), isolated, engine.Options{
		Database: testDB(t, "verified"), Identity: iamUser(t), Region: "us-east-1",
		Simulator: &fakeSimulator{deny: map[string]bool{"s3:CreateBucket": true}},
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if only.Counts().Denied != 0 {
		t.Errorf("Counts().Denied = %d, want 0 — the only denial is inconclusive", only.Counts().Denied)
	}
	if only.ShouldFail(finding.FailOnDenied) {
		t.Error("ShouldFail(denied) = true on a report whose only denial is inconclusive")
	}
}

// TestConclusiveDenialStillFails is the other half: suppressing inconclusive
// denials must not suppress real ones.
func TestConclusiveDenialStillFails(t *testing.T) {
	rep := analyze(t, engine.Options{
		Database:  testDB(t, "verified"),
		Simulator: &fakeSimulator{deny: map[string]bool{"s3:CreateBucket": true}},
	})

	// logs has a literal bucket name, so its ARN is exact and the denial stands.
	f := findingFor(t, rep, "aws_s3_bucket.logs", "create")
	if !f.Denied() {
		t.Fatal("denial against an exactly-known ARN was suppressed")
	}
	if got := f.MissingActions(); len(got) != 1 || got[0] != "s3:CreateBucket" {
		t.Errorf("MissingActions() = %v, want [s3:CreateBucket]", got)
	}
	if !rep.ShouldFail(finding.FailOnDenied) {
		t.Error("ShouldFail(denied) = false despite a conclusive missing permission")
	}
}

// ---------------------------------------------------------------------------
// The silent-allow trap
// ---------------------------------------------------------------------------

// TestSilentAllowTrapDowngrades is the most important new test in the milestone.
//
// Measured (R1): a policy gated on a condition key we did not supply can return
// ALLOWED with an EMPTY MissingContextValues, because the simulator substitutes
// its own value. Nothing in the response says a key was involved. The only
// signal is GetContextKeysForPrincipalPolicy telling us up front that the key is
// referenced at all — so an unsupplied referenced key must downgrade confidence
// regardless of the decision.
func TestSilentAllowTrapDowngrades(t *testing.T) {
	rep := analyze(t, engine.Options{
		Database:  testDB(t, "verified"),
		Simulator: &fakeSimulator{}, // allows everything, reports nothing missing
		// The principal's policies reference a tag condition. The fixture's
		// buckets carry no tags, so we cannot source a value.
		PolicyContextKeys: []string{"aws:RequestTag/Environment"},
	})

	f := findingFor(t, rep, "aws_s3_bucket.logs", "create")
	if f.Denied() {
		t.Fatal("fake allows everything; nothing should be denied")
	}
	requireReason(t, f, finding.ReasonConditionKeysUnknown)
	if f.Level == finding.LevelVerified {
		t.Error("claimed Verified while a referenced condition key was unsupplied")
	}
	found := false
	for _, k := range f.UnsuppliedContextKeys {
		if k == "aws:RequestTag/Environment" {
			found = true
		}
	}
	if !found {
		t.Errorf("UnsuppliedContextKeys = %v, want it to name aws:RequestTag/Environment",
			f.UnsuppliedContextKeys)
	}
}

// TestSourcedContextKeyDoesNotDowngrade is the control: when we CAN source the
// value, there is no trap and no downgrade.
func TestSourcedContextKeyDoesNotDowngrade(t *testing.T) {
	db := testDB(t, "verified")
	p := &plan.Plan{
		FormatVersion: "1.2",
		ResourceChanges: []plan.ResourceChange{{
			Address: "aws_iam_role.tagged", Mode: "managed", Type: "aws_iam_role",
			Name: "tagged", ProviderName: "registry.terraform.io/hashicorp/aws",
			Change: plan.Change{
				Actions: []plan.Action{plan.ActionCreate},
				After:   map[string]any{"name": "r", "tags": map[string]any{"Environment": "prod"}},
			},
		}},
	}
	// aws_iam_role in testDB declares no context_keys, so add one via a DB that does.
	doc := `
resources:
  - type: aws_iam_role
    service: iam
    status: verified
    arn_format: "arn:${Partition}:iam::${Account}:role/${RoleName}"
    arn_attributes: { RoleName: name }
    context_keys:
      "aws:RequestTag/*": { from: tags }
    operations:
      create: [iam:CreateRole]
`
	var err error
	db, err = mapping.Load(fstest.MapFS{"t.yaml": &fstest.MapFile{Data: []byte(doc)}})
	if err != nil {
		t.Fatalf("loading db: %v", err)
	}

	sim := &fakeSimulator{}
	rep, err := engine.Analyze(context.Background(), p, engine.Options{
		Database:          db,
		Identity:          iamUser(t),
		Region:            "us-east-1",
		Simulator:         sim,
		PolicyContextKeys: []string{"aws:RequestTag/Environment"},
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	f := findingFor(t, rep, "aws_iam_role.tagged", "create")
	if hasReason(f, finding.ReasonConditionKeysUnknown) {
		t.Errorf("downgraded despite the value being sourceable: %v", f.Reasons)
	}
	requireLevel(t, f, finding.LevelVerified)

	// And the value actually reached the simulator.
	var sawTag bool
	for _, e := range sim.requests[0].Items[0].Context {
		if e.Key == "aws:RequestTag/Environment" && len(e.Values) == 1 && e.Values[0] == "prod" {
			sawTag = true
		}
	}
	if !sawTag {
		t.Errorf("tag value was not supplied: %+v", sim.requests[0].Items[0].Context)
	}
}

// TestRequestedRegionAlwaysSupplied guards the mitigation directly. Letting this
// key default is what arms the trap.
func TestRequestedRegionAlwaysSupplied(t *testing.T) {
	sim := &fakeSimulator{}
	analyze(t, engine.Options{
		Database:  testDB(t, "verified"),
		Simulator: sim,
		Region:    "eu-west-1",
	})

	if len(sim.requests) != 1 {
		t.Fatalf("expected exactly one Resolve call, got %d", len(sim.requests))
	}
	for i, item := range sim.requests[0].Items {
		var got string
		for _, e := range item.Context {
			if e.Key == engine.RequestedRegionKey {
				got = strings.Join(e.Values, ",")
			}
		}
		if got != "eu-west-1" {
			t.Errorf("item %d: aws:RequestedRegion = %q, want eu-west-1 (context: %+v)",
				i, got, item.Context)
		}
	}
}

// TestNoRegionIsACaveatNotAnOmission — when no region is determinable, omitting
// the key silently would re-arm the trap. It must become a visible caveat.
func TestNoRegionIsACaveatNotAnOmission(t *testing.T) {
	rep := analyze(t, engine.Options{
		Database:  testDB(t, "verified"),
		Simulator: &fakeSimulator{},
		Region:    " ", // whitespace is not a region; normalised to empty below
	})
	_ = rep

	rep2, err := engine.Analyze(context.Background(), testPlan(t), engine.Options{
		Database:  testDB(t, "verified"),
		Identity:  iamUser(t),
		Simulator: &fakeSimulator{},
		// Region deliberately empty.
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	f := findingFor(t, rep2, "aws_s3_bucket.logs", "create")
	requireReason(t, f, finding.ReasonConditionKeysUnknown)
	found := false
	for _, k := range f.UnsuppliedContextKeys {
		if k == engine.RequestedRegionKey {
			found = true
		}
	}
	if !found {
		t.Errorf("UnsuppliedContextKeys = %v, want it to name %s",
			f.UnsuppliedContextKeys, engine.RequestedRegionKey)
	}
	if f.Level == finding.LevelVerified {
		t.Error("claimed Verified with no region supplied")
	}
}

// TestDenialWithMissingContextIsSuppressed — C1: a denial that names a key we
// did not supply proves only that we did not supply it.
func TestDenialWithMissingContextIsSuppressed(t *testing.T) {
	rep := analyze(t, engine.Options{
		Database: testDB(t, "verified"),
		Simulator: &fakeSimulator{
			deny:       map[string]bool{"iam:CreateRole": true},
			missingCtx: map[string][]string{"iam:CreateRole": {"aws:RequestTag/Environment"}},
		},
	})

	f := findingFor(t, rep, "aws_iam_role.deploy", "create")
	if f.Denied() {
		t.Error("a denial naming a missing context key was reported as a missing permission")
	}
	requireReason(t, f, finding.ReasonConditionKeysUnknown)
	requireLevel(t, f, finding.LevelUnchecked)

	sup := f.SuppressedDenials()
	if len(sup) != 1 || sup[0].InconclusiveReason != finding.ReasonConditionKeysUnknown {
		t.Errorf("SuppressedDenials() = %+v", sup)
	}
	if !rep.ShouldFail(finding.FailOnUnchecked) {
		t.Error("an unchecked finding should still trip the strict threshold")
	}
}

// TestDenialWithSuppliedContextIsReal guards against over-suppressing. C3: when
// we DID supply a value and the policy still denied, that is a real finding.
func TestDenialWithSuppliedContextIsReal(t *testing.T) {
	rep := analyze(t, engine.Options{
		Database: testDB(t, "verified"),
		// Denied, and AWS reports nothing missing — we supplied what it needed.
		Simulator: &fakeSimulator{deny: map[string]bool{"iam:CreateRole": true}},
	})

	f := findingFor(t, rep, "aws_iam_role.deploy", "create")
	if !f.Denied() {
		t.Fatal("a genuine denial was suppressed")
	}
	if got := f.MissingActions(); len(got) != 1 || got[0] != "iam:CreateRole" {
		t.Errorf("MissingActions() = %v", got)
	}
}

// ---------------------------------------------------------------------------
// Levels
// ---------------------------------------------------------------------------

func TestAnalyzeVerified(t *testing.T) {
	rep := analyze(t, engine.Options{
		Database:  testDB(t, "verified"),
		Simulator: &fakeSimulator{},
	})

	f := findingFor(t, rep, "aws_iam_role.deploy", "create")
	requireLevel(t, f, finding.LevelVerified)
	if len(f.Reasons) != 0 {
		t.Errorf("Verified finding carries reasons: %v", f.Reasons)
	}
}

// TestVerifiedRequiresEveryClearance drives each caveat individually to prove
// that none of them alone permits Verified. This is docs/DESIGN.md §3.3 written
// as a test at the level that matters.
func TestVerifiedRequiresEveryClearance(t *testing.T) {
	base := func() engine.Options {
		return engine.Options{Database: testDB(t, "verified"), Simulator: &fakeSimulator{}}
	}

	tests := map[string]struct {
		mutate func(*engine.Options)
		addr   string
	}{
		"draft mapping": {
			func(o *engine.Options) { o.Database = testDB(t, "draft") },
			"aws_iam_role.deploy",
		},
		"unsupplied context key": {
			func(o *engine.Options) { o.PolicyContextKeys = []string{"aws:SourceIp"} },
			"aws_iam_role.deploy",
		},
		"context keys unknowable": {
			func(o *engine.Options) { o.ContextKeysUnknown = true },
			"aws_iam_role.deploy",
		},
		"no region": {
			func(o *engine.Options) { o.Region = "-" }, // replaced below
			"aws_iam_role.deploy",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			opts := base()
			tt.mutate(&opts)
			if opts.Region == "-" {
				opts.Region = ""
			}
			opts.Identity = iamUser(t)
			if opts.Region == "" && name != "no region" {
				opts.Region = "us-east-1"
			}
			rep, err := engine.Analyze(context.Background(), testPlan(t), opts)
			if err != nil {
				t.Fatalf("Analyze: %v", err)
			}
			f := findingFor(t, rep, tt.addr, "create")
			if f.Level == finding.LevelVerified {
				t.Errorf("claimed Verified despite %s (reasons: %v)", name, f.Reasons)
			}
		})
	}
}

func TestAnalyzeDraftMappingCapsAtLikely(t *testing.T) {
	rep := analyze(t, engine.Options{
		Database:  testDB(t, "draft"),
		Simulator: &fakeSimulator{},
	})
	f := findingFor(t, rep, "aws_iam_role.deploy", "create")
	requireLevel(t, f, finding.LevelLikely)
	requireReason(t, f, finding.ReasonMappingUnverified)
}

func TestAnalyzeResourcePolicyAndRCPForceLikely(t *testing.T) {
	rep := analyze(t, engine.Options{
		Database:  testDB(t, "verified"),
		Simulator: &fakeSimulator{},
	})

	f := findingFor(t, rep, "aws_s3_bucket.logs", "create")
	requireLevel(t, f, finding.LevelLikely)
	requireReason(t, f, finding.ReasonResourcePolicyNotEvaluated)
	requireReason(t, f, finding.ReasonRCPNotEvaluated)
	if hasReason(f, finding.ReasonARNUnresolved) {
		t.Error("ARN reported unresolved, but the fixture supplies a literal bucket name")
	}
}

// TestWildcardAllowIsLikely — the informative direction. An allow against "*"
// means the policy grants broadly enough to cover any name the bucket ends up
// with, which is worth reporting rather than discarding.
func TestWildcardAllowIsLikely(t *testing.T) {
	sim := &fakeSimulator{}
	rep := analyze(t, engine.Options{
		Database:  testDB(t, "verified"),
		Simulator: sim,
	})

	f := findingFor(t, rep, "aws_s3_bucket.artifacts", "create")
	requireLevel(t, f, finding.LevelLikely)
	requireReason(t, f, finding.ReasonARNUnresolved)

	var sawWildcard bool
	for _, item := range sim.requests[0].Items {
		if item.ResourceARN == "*" {
			sawWildcard = true
		}
	}
	if !sawWildcard {
		t.Error("expected a question scoped to * for the unknown bucket name")
	}
}

// TestReadActionsApplyToEveryOperation pins the finding that motivated the
// read_actions field.
//
// Measured 2026-09-01: creating a tagged aws_s3_bucket needs 17 IAM actions, 14
// of which are reads Terraform makes when it reads the resource back. The write
// action alone is not sufficient for ANY operation, so the read set has to be
// unioned into all of them — otherwise the tool reports "allowed" for an apply
// that cannot succeed.
func TestReadActionsApplyToEveryOperation(t *testing.T) {
	doc := `
resources:
  - type: aws_s3_bucket
    service: s3
    status: verified
    arn_format: "arn:${Partition}:s3:::${BucketName}"
    arn_attributes: { BucketName: bucket }
    read_actions: [s3:ListBucket, s3:GetBucketTagging]
    operations:
      create: [s3:CreateBucket]
      delete: [s3:DeleteBucket]
`
	db, err := mapping.Load(fstest.MapFS{"t.yaml": &fstest.MapFile{Data: []byte(doc)}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	sim := &fakeSimulator{}
	p := &plan.Plan{
		FormatVersion: "1.2",
		ResourceChanges: []plan.ResourceChange{{
			Address: "aws_s3_bucket.b", Mode: "managed", Type: "aws_s3_bucket",
			Name: "b", ProviderName: "registry.terraform.io/hashicorp/aws",
			Change: plan.Change{
				Actions: []plan.Action{plan.ActionDelete, plan.ActionCreate},
				Before:  map[string]any{"bucket": "old"},
				After:   map[string]any{"bucket": "new"},
			},
		}},
	}
	if _, err := engine.Analyze(context.Background(), p, engine.Options{
		Database: db, Identity: iamUser(t), Region: "us-east-1", Simulator: sim,
	}); err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	if len(sim.requests) != 1 || len(sim.requests[0].Items) != 2 {
		t.Fatalf("expected two questions (create and delete), got %+v", sim.requests)
	}
	for _, item := range sim.requests[0].Items {
		got := strings.Join(item.Actions, ",")
		for _, want := range []string{"s3:ListBucket", "s3:GetBucketTagging"} {
			if !strings.Contains(got, want) {
				t.Errorf("operation asked for [%s], missing read action %s", got, want)
			}
		}
	}
}

func TestAnalyzeUnmappedTypeIsUnchecked(t *testing.T) {
	rep := analyze(t, engine.Options{
		Database:  testDB(t, "verified"),
		Simulator: &fakeSimulator{},
	})
	f := findingFor(t, rep, "aws_dynamodb_table.state_lock", "update")
	requireLevel(t, f, finding.LevelUnchecked)
	requireReason(t, f, finding.ReasonNoMapping)
}

func TestAnalyzeOperationNotMapped(t *testing.T) {
	p := &plan.Plan{
		FormatVersion: "1.2",
		ResourceChanges: []plan.ResourceChange{{
			Address: "aws_iam_role.tweaked", Mode: "managed", Type: "aws_iam_role",
			Name: "tweaked", ProviderName: "registry.terraform.io/hashicorp/aws",
			Change: plan.Change{
				Actions: []plan.Action{plan.ActionUpdate},
				Before:  map[string]any{"name": "r"},
				After:   map[string]any{"name": "r"},
			},
		}},
	}
	rep, err := engine.Analyze(context.Background(), p, engine.Options{
		Database: testDB(t, "verified"), Identity: iamUser(t),
		Region: "us-east-1", Simulator: &fakeSimulator{},
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	f := findingFor(t, rep, "aws_iam_role.tweaked", "update")
	requireLevel(t, f, finding.LevelUnchecked)
	requireReason(t, f, finding.ReasonOperationNotMapped)
}

func TestAnalyzeReplaceChecksBothOperations(t *testing.T) {
	rep := analyze(t, engine.Options{
		Database:  testDB(t, "verified"),
		Simulator: &fakeSimulator{},
	})
	for _, op := range []string{"create", "delete"} {
		findingFor(t, rep, "aws_iam_role.deploy", op)
	}
}

// ---------------------------------------------------------------------------
// Failure handling
// ---------------------------------------------------------------------------

func TestAnalyzeSimulatorErrorIsUnchecked(t *testing.T) {
	rep := analyze(t, engine.Options{
		Database:  testDB(t, "verified"),
		Simulator: &fakeSimulator{err: fmt.Errorf("connection reset")},
	})
	for _, f := range rep.Findings {
		if f.Level != finding.LevelUnchecked {
			t.Errorf("%s %s: Level = %q after a simulator error, want unchecked",
				f.ResourceAddress, f.Operation, f.Level)
		}
	}
	if len(rep.Warnings) == 0 {
		t.Error("a simulator error produced no warning")
	}
}

// TestPartialBatchFailureIsUnchecked — one throttled batch must degrade only
// its own findings, not the whole run.
func TestPartialBatchFailureIsUnchecked(t *testing.T) {
	rep := analyze(t, engine.Options{
		Database:  testDB(t, "verified"),
		Simulator: &fakeSimulator{failItems: map[int]bool{0: true}},
	})

	var unchecked, other int
	for _, f := range rep.Findings {
		if hasReason(f, finding.ReasonSimulationFailed) {
			unchecked++
			if f.Level != finding.LevelUnchecked {
				t.Errorf("%s: failed item is %q, want unchecked", f.ResourceAddress, f.Level)
			}
		} else if f.Level == finding.LevelVerified || f.Level == finding.LevelLikely {
			other++
		}
	}
	if unchecked == 0 {
		t.Error("the failed item did not degrade")
	}
	if other == 0 {
		t.Error("a single failed batch degraded findings it should not have")
	}
}

// TestResolveContractViolationIsUnchecked — a simulator that breaks index
// alignment must not cause a panic or a mismatched answer.
func TestResolveContractViolationIsUnchecked(t *testing.T) {
	rep := analyze(t, engine.Options{
		Database:  testDB(t, "verified"),
		Simulator: &fakeSimulator{dropLast: true},
	})
	for _, f := range rep.Findings {
		if f.Level == finding.LevelVerified || f.Level == finding.LevelLikely {
			t.Errorf("%s: Level = %q despite unmatched outcomes", f.ResourceAddress, f.Level)
		}
	}
	if len(rep.Warnings) == 0 {
		t.Error("a contract violation produced no warning")
	}
}

func TestAnalyzeWithoutSimulatorIsAllUnchecked(t *testing.T) {
	rep := analyze(t, engine.Options{
		Database:  testDB(t, "verified"),
		Simulator: nil,
	})
	if len(rep.Findings) == 0 {
		t.Fatal("no findings produced")
	}
	for _, f := range rep.Findings {
		requireLevel(t, f, finding.LevelUnchecked)
	}
	if len(rep.Warnings) == 0 {
		t.Error("running without a simulator produced no warning")
	}
	if !rep.ShouldFail(finding.FailOnUnchecked) {
		t.Error("ShouldFail(unchecked) = false despite every finding being unchecked")
	}
}

func TestAnalyzeRequiresDatabase(t *testing.T) {
	if _, err := engine.Analyze(context.Background(), testPlan(t), engine.Options{}); err == nil {
		t.Error("Analyze accepted Options with no Database")
	}
}

// ---------------------------------------------------------------------------
// Structure
// ---------------------------------------------------------------------------

// TestUnmappedTypesConsumeNoRequestSlot — resources we were never going to
// check must not dilute batching.
func TestUnmappedTypesConsumeNoRequestSlot(t *testing.T) {
	sim := &fakeSimulator{}
	rep := analyze(t, engine.Options{
		Database:  testDB(t, "verified"),
		Simulator: sim,
	})

	var simulatable int
	for _, f := range rep.Findings {
		if !hasReason(f, finding.ReasonNoMapping) && !hasReason(f, finding.ReasonOperationNotMapped) {
			simulatable++
		}
	}
	if got := len(sim.requests[0].Items); got != simulatable {
		t.Errorf("Request carried %d items for %d simulatable findings", got, simulatable)
	}
}

func TestFindingsStayInPlanOrder(t *testing.T) {
	rep := analyze(t, engine.Options{
		Database:  testDB(t, "verified"),
		Simulator: &fakeSimulator{},
	})
	want := []string{
		"aws_s3_bucket.logs", "aws_s3_bucket.artifacts",
		"aws_iam_role.deploy", "aws_iam_role.deploy",
		"aws_dynamodb_table.state_lock",
	}
	if len(rep.Findings) != len(want) {
		t.Fatalf("got %d findings, want %d", len(rep.Findings), len(want))
	}
	for i, w := range want {
		if rep.Findings[i].ResourceAddress != w {
			t.Errorf("finding %d = %q, want %q", i, rep.Findings[i].ResourceAddress, w)
		}
	}
}

// TestActionsAreSortedAndDeduped — identical action sets must hash identically
// or batching silently fragments.
func TestActionsAreSortedAndDeduped(t *testing.T) {
	sim := &fakeSimulator{}
	analyze(t, engine.Options{Database: testDB(t, "verified"), Simulator: sim})

	for _, item := range sim.requests[0].Items {
		for i := 1; i < len(item.Actions); i++ {
			if item.Actions[i-1] >= item.Actions[i] {
				t.Errorf("actions not sorted and deduped: %v", item.Actions)
				break
			}
		}
	}
}

// TestNoSCPDowngrade is a regression guard on docs/DESIGN.md §0.1. The original
// spec assumed SCPs were unsimulatable and downgraded on that basis, which made
// Verified unreachable in any account belonging to an Organization.
// SimulatePrincipalPolicy evaluates in-scope SCPs itself.
func TestNoSCPDowngrade(t *testing.T) {
	rep := analyze(t, engine.Options{
		Database:  testDB(t, "verified"),
		Simulator: &fakeSimulator{},
	})
	for _, f := range rep.Findings {
		for _, r := range f.Reasons {
			if strings.Contains(strings.ToLower(string(r)), "scp") {
				t.Errorf("%s: SCP-based reason %q reappeared", f.ResourceAddress, r)
			}
		}
	}
}
