package derive_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Caleb-Kelly-25/preflight/internal/derive"
)

// fakeGrantor records what was granted. No AWS.
type fakeGrantor struct {
	granted [][]string
	revokes int
	failOn  int // attempt index to fail, -1 for never
}

func (g *fakeGrantor) Grant(_ context.Context, actions []string) error {
	g.granted = append(g.granted, append([]string(nil), actions...))
	if g.failOn >= 0 && len(g.granted)-1 == g.failOn {
		return fmt.Errorf("simulated grant failure")
	}
	return nil
}
func (g *fakeGrantor) Revoke(context.Context) error { g.revokes++; return nil }

// fakeApplier answers from a "true" required set, which is what makes the whole
// loop testable: give it the answer and assert the loop recovers it.
type fakeApplier struct {
	grantor *fakeGrantor
	// required is the set an apply genuinely needs.
	required []string
	// hangsOn is an action whose absence stalls rather than denies, modelling
	// s3:ListBucket and the HeadBucket retry loop.
	hangsOn string
	// opaque suppresses action names in denials, modelling EC2.
	opaque bool

	// destroyFailsAfter makes Destroy start failing at the Nth call, modelling
	// a stalled apply that leaves the state lock held.
	destroyFailsAfter int

	applies  int
	destroys int
}

func (a *fakeApplier) currentGrant() []string {
	if len(a.grantor.granted) == 0 {
		return nil
	}
	return a.grantor.granted[len(a.grantor.granted)-1]
}

func (a *fakeApplier) Apply(context.Context, time.Duration) derive.Outcome {
	a.applies++
	granted := a.currentGrant()
	var missing []string
	for _, need := range a.required {
		if !slices.Contains(granted, need) {
			missing = append(missing, need)
		}
	}
	switch {
	case len(missing) == 0:
		return derive.Outcome{Kind: derive.OutcomeSuccess}
	case a.hangsOn != "" && slices.Contains(missing, a.hangsOn):
		return derive.Outcome{Kind: derive.OutcomeStalled, StalledAt: "fake.resource"}
	case a.opaque:
		return derive.Outcome{Kind: derive.OutcomeOpaque}
	default:
		return derive.Outcome{
			Kind:          derive.OutcomeDenied,
			DeniedActions: missing[:1], // AWS reports one at a time
			Detail:        "not authorized to perform: " + missing[0],
		}
	}
}

func (a *fakeApplier) Destroy(context.Context) error {
	a.destroys++
	if a.destroyFailsAfter > 0 && a.destroys >= a.destroyFailsAfter {
		return fmt.Errorf("simulated destroy failure")
	}
	return nil
}

func newFixture(required []string, seed []string) (*derive.Deriver, *fakeGrantor, *fakeApplier) {
	g := &fakeGrantor{failOn: -1}
	a := &fakeApplier{grantor: g, required: required}
	d := &derive.Deriver{Grantor: g, Applier: a, MaxAttempts: 40, AttemptBudget: time.Second}
	_ = seed
	return d, g, a
}

// TestZeroOutcomeIsNotSuccess is this package's analogue of
// TestNoDefaultVerified: a result nobody filled in must never read as a pass.
func TestZeroOutcomeIsNotSuccess(t *testing.T) {
	var o derive.Outcome
	if o.Kind == derive.OutcomeSuccess {
		t.Fatal("the zero Outcome reports success")
	}
	if got := o.Kind.String(); got != "unknown" {
		t.Errorf("zero Outcome kind = %q, want %q", got, "unknown")
	}
	var c derive.Confidence
	if c == derive.ConfidenceProven {
		t.Fatal("the zero Confidence claims proof")
	}
}

// TestDeriveRecoversTheRealSet is the acceptance test named in the plan: replay
// the aws_s3_bucket ground truth, which was established entirely by hand, and
// confirm the loop independently arrives at the same 17 actions — including via
// the hang path, since omitting s3:ListBucket stalls rather than denies.
func TestDeriveRecoversTheRealSet(t *testing.T) {
	truth := []string{
		"s3:CreateBucket", "s3:DeleteBucket", "s3:PutBucketTagging", "s3:ListBucket",
		"s3:GetAccelerateConfiguration", "s3:GetBucketAcl", "s3:GetBucketCORS",
		"s3:GetBucketLogging", "s3:GetBucketObjectLockConfiguration", "s3:GetBucketPolicy",
		"s3:GetBucketRequestPayment", "s3:GetBucketTagging", "s3:GetBucketVersioning",
		"s3:GetBucketWebsite", "s3:GetEncryptionConfiguration", "s3:GetLifecycleConfiguration",
		"s3:GetReplicationConfiguration",
	}
	// The mapping as it actually was before the first derivation: 3 of 17.
	seed := []string{"s3:CreateBucket", "s3:DeleteBucket", "s3:PutBucketTagging"}

	d, _, _ := newFixture(truth, seed)
	res, err := d.Derive(context.Background(), seed)
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if res.Sufficiency != derive.ConfidenceProven {
		t.Fatalf("sufficiency = %s, want proven (warnings: %v)", res.Sufficiency, res.Warnings)
	}

	slices.Sort(truth)
	if !slices.Equal(res.Sufficient, truth) {
		t.Errorf("derived set does not match ground truth\n got: %v\nwant: %v", res.Sufficient, truth)
	}
	if len(res.Missing) != 14 {
		t.Errorf("Missing = %d actions, want 14 (the gap the first manual run found): %v",
			len(res.Missing), res.Missing)
	}
	if len(res.Surplus) != 0 {
		t.Errorf("Surplus = %v, want none", res.Surplus)
	}
}

// The hang is the failure mode a naive harness gets wrong, so it gets its own
// test. A stall is BOTH positive evidence about the removed action AND a reason
// to stop, because ending the stall means killing the apply.
func TestHangIsEvidenceAndStopsTheRun(t *testing.T) {
	truth := []string{"s3:CreateBucket", "s3:ListBucket"}
	g := &fakeGrantor{failOn: -1}
	a := &fakeApplier{grantor: g, required: truth, hangsOn: "s3:ListBucket"}
	d := &derive.Deriver{Grantor: g, Applier: a, MaxAttempts: 20, AttemptBudget: time.Second}

	// A stall records its evidence AND stops the run. Both halves matter.
	//
	// The evidence is sound: the apply that stalled proves the removed action was
	// load-bearing, which is the finding. But the apply was KILLED to end the
	// stall, so state no longer describes reality — Terraform may have created
	// the resource without recording it, in which case destroy finds nothing,
	// reports success, and leaves it behind.
	//
	// Continuing past that measures the next action against a dirty account while
	// believing it is clean. That really happened: the attempt after
	// s3:ListBucket's stall blamed an action that a separate targeted experiment
	// showed was droppable, entirely because of a leftover bucket.
	res, err := d.Derive(context.Background(), truth)
	if err == nil {
		t.Fatal("a stall did not stop the run; later attempts would measure against unknown state")
	}
	if !res.Dirty {
		t.Error("Result.Dirty is false after a stall killed an apply")
	}

	ev, ok := res.Evidence["s3:ListBucket"]
	if !ok {
		t.Fatal("the stall's own evidence was discarded; it is the finding")
	}
	if ev.Kind != derive.OutcomeStalled {
		t.Errorf("evidence kind = %s, want stalled", ev.Kind)
	}
	// Weaker evidence than a clean denial, and the result must say so.
	if res.Minimal == derive.ConfidenceProven {
		t.Error("minimality reported as proven despite resting on a hang")
	}
	if len(res.Warnings) == 0 {
		t.Error("no warning explains why the run stopped or how to resume")
	}
}

// A hang during discovery names no action, so the loop must stop and say so
// rather than invent one.
func TestStallDuringDiscoveryIsInconclusive(t *testing.T) {
	truth := []string{"s3:CreateBucket", "s3:ListBucket"}
	g := &fakeGrantor{failOn: -1}
	a := &fakeApplier{grantor: g, required: truth, hangsOn: "s3:ListBucket"}
	d := &derive.Deriver{Grantor: g, Applier: a, MaxAttempts: 20, AttemptBudget: time.Second}

	res, err := d.Derive(context.Background(), []string{"s3:CreateBucket"})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if res.Sufficiency != derive.ConfidenceInconclusive {
		t.Errorf("sufficiency = %s, want inconclusive", res.Sufficiency)
	}
	if len(res.Sufficient) != 0 {
		t.Errorf("Sufficient = %v, want empty: nothing was proven", res.Sufficient)
	}
}

// An over-reporting seed must be trimmed, and the surplus reported.
func TestSurplusIsRemoved(t *testing.T) {
	truth := []string{"ec2:CreateVpc", "ec2:DescribeVpcs"}
	seed := append(append([]string(nil), truth...), "ec2:DescribeTags") // the real aws_vpc over-report
	d, _, _ := newFixture(truth, seed)

	res, err := d.Derive(context.Background(), seed)
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if !slices.Equal(res.Surplus, []string{"ec2:DescribeTags"}) {
		t.Errorf("Surplus = %v, want [ec2:DescribeTags]", res.Surplus)
	}
	if slices.Contains(res.Sufficient, "ec2:DescribeTags") {
		t.Error("an unnecessary action survived minimisation")
	}
}

// Teardown must run after every attempt, including failed ones. A leaked
// resource makes the next attempt lie.
func TestEveryAttemptTearsDown(t *testing.T) {
	truth := []string{"s3:CreateBucket", "s3:GetBucketAcl"}
	d, _, a := newFixture(truth, truth)
	if _, err := d.Derive(context.Background(), []string{"s3:CreateBucket"}); err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if a.destroys != a.applies {
		t.Errorf("destroys = %d, applies = %d; every apply must be torn down", a.destroys, a.applies)
	}
	if a.applies == 0 {
		t.Fatal("no applies ran")
	}
}

func TestOpaqueDenialStopsRatherThanGuessing(t *testing.T) {
	truth := []string{"ec2:CreateVpc", "ec2:DeleteVpc"}
	g := &fakeGrantor{failOn: -1}
	a := &fakeApplier{grantor: g, required: truth, opaque: true}
	d := &derive.Deriver{Grantor: g, Applier: a, MaxAttempts: 10, AttemptBudget: time.Second}

	res, err := d.Derive(context.Background(), []string{"ec2:CreateVpc"})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if res.Sufficiency != derive.ConfidenceInconclusive {
		t.Errorf("sufficiency = %s, want inconclusive", res.Sufficiency)
	}
	if len(res.Warnings) == 0 || !strings.Contains(strings.Join(res.Warnings, " "), "DecodeAuthorizationMessage") {
		t.Errorf("warnings do not explain the opaque denial: %v", res.Warnings)
	}
}

// TestFailedTeardownAbortsTheRun covers the bug the first live run exposed.
//
// A destroy that fails leaves infrastructure behind, and the next attempt then
// measures against it. An apply that "succeeds" only because the resource
// already exists reads as "that action was not needed" — which writes an action
// set missing something real. Stopping is the only safe response.
func TestFailedTeardownAbortsTheRun(t *testing.T) {
	truth := []string{"ec2:CreateVpc", "ec2:DescribeVpcs", "ec2:ModifyVpcAttribute"}
	g := &fakeGrantor{failOn: -1}
	a := &fakeApplier{grantor: g, required: truth, destroyFailsAfter: 2}
	d := &derive.Deriver{Grantor: g, Applier: a, MaxAttempts: 20, AttemptBudget: time.Second}

	res, err := d.Derive(context.Background(), truth)
	if err == nil {
		t.Fatal("Derive returned no error despite a failed teardown")
	}
	if !res.Dirty {
		t.Error("Result.Dirty is false after a teardown failure")
	}
	// It must stop, not carry on through the remaining removals.
	if a.applies > 3 {
		t.Errorf("kept going after teardown failed: %d applies", a.applies)
	}
	if res.Minimal == derive.ConfidenceProven {
		t.Error("minimality claimed proven on a dirty run")
	}
	if len(res.Warnings) == 0 {
		t.Error("no warning explains why the run stopped")
	}
}

func TestDeriveRequiresItsCollaborators(t *testing.T) {
	var d derive.Deriver
	if _, err := d.Derive(context.Background(), nil); err == nil {
		t.Error("Derive ran with no Grantor or Applier")
	}
}
