package finding

import "testing"

// TestNoDefaultVerified enforces the invariant in docs/DESIGN.md §3.3: no
// finding reaches Verified by default, by fallthrough, or on error. A finding
// that nobody classified must be treated as unchecked everywhere, not merely
// be unequal to Verified.
func TestNoDefaultVerified(t *testing.T) {
	var f Finding

	if f.Level == LevelVerified {
		t.Fatal("the zero-value Finding is Verified")
	}
	if f.Level.Rank() != LevelUnchecked.Rank() {
		t.Errorf("zero-value Level ranks %d, want the same as Unchecked (%d)",
			f.Level.Rank(), LevelUnchecked.Rank())
	}

	r := &Report{Findings: []Finding{{}}}
	if c := r.Counts(); c.Unchecked != 1 || c.Verified != 0 || c.Likely != 0 {
		t.Errorf("zero-value finding counted as %+v, want exactly one unchecked", c)
	}
	if !r.ShouldFail(FailOnUnchecked) {
		t.Error("ShouldFail(unchecked) = false for a report of unclassified findings")
	}
}

func TestLevelRank(t *testing.T) {
	if !(LevelVerified.Rank() < LevelLikely.Rank() && LevelLikely.Rank() < LevelUnchecked.Rank()) {
		t.Errorf("levels do not rank verified < likely < unchecked: %d, %d, %d",
			LevelVerified.Rank(), LevelLikely.Rank(), LevelUnchecked.Rank())
	}
}

func TestDecisionDenied(t *testing.T) {
	for d, want := range map[Decision]bool{
		DecisionAllowed:      false,
		DecisionImplicitDeny: true,
		DecisionExplicitDeny: true,
		DecisionNotSimulated: false,
	} {
		if got := d.Denied(); got != want {
			t.Errorf("%q.Denied() = %v, want %v", d, got, want)
		}
	}
}

func TestAddReasonDeduplicatesAndSorts(t *testing.T) {
	var f Finding
	f.AddReason(ReasonRCPNotEvaluated)
	f.AddReason(ReasonARNUnresolved)
	f.AddReason(ReasonRCPNotEvaluated)

	if len(f.Reasons) != 2 {
		t.Fatalf("Reasons = %v, want 2 unique entries", f.Reasons)
	}
	if f.Reasons[0] > f.Reasons[1] {
		t.Errorf("Reasons not sorted: %v", f.Reasons)
	}
}

func TestShouldFailThresholds(t *testing.T) {
	denied := Finding{Level: LevelVerified, Actions: []ActionResult{
		{Action: "s3:CreateBucket", Decision: DecisionImplicitDeny},
	}}
	likely := Finding{Level: LevelLikely}
	unchecked := Finding{Level: LevelUnchecked}
	clean := Finding{Level: LevelVerified}

	tests := []struct {
		name      string
		findings  []Finding
		threshold FailThreshold
		want      bool
	}{
		// A denial fails at every threshold: it is the one state we are certain
		// about, so no configuration may let it through.
		{"denial fails on denied", []Finding{denied}, FailOnDenied, true},
		{"denial fails on likely", []Finding{denied}, FailOnLikely, true},
		{"denial fails on unchecked", []Finding{denied}, FailOnUnchecked, true},

		{"likely passes on denied", []Finding{likely}, FailOnDenied, false},
		{"likely fails on likely", []Finding{likely}, FailOnLikely, true},
		{"likely passes on unchecked", []Finding{likely}, FailOnUnchecked, false},

		{"unchecked passes on denied", []Finding{unchecked}, FailOnDenied, false},
		{"unchecked fails on likely", []Finding{unchecked}, FailOnLikely, true},
		{"unchecked fails on unchecked", []Finding{unchecked}, FailOnUnchecked, true},

		{"clean passes everywhere", []Finding{clean}, FailOnUnchecked, false},
		{"empty report passes", nil, FailOnUnchecked, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Report{Findings: tt.findings}
			if got := r.ShouldFail(tt.threshold); got != tt.want {
				t.Errorf("ShouldFail(%q) = %v, want %v", tt.threshold, got, tt.want)
			}
		})
	}
}

func TestMissingActions(t *testing.T) {
	f := Finding{Actions: []ActionResult{
		{Action: "s3:CreateBucket", Decision: DecisionAllowed},
		{Action: "s3:PutBucketTagging", Decision: DecisionImplicitDeny},
		{Action: "s3:DeleteBucket", Decision: DecisionExplicitDeny},
	}}
	got := f.MissingActions()
	if len(got) != 2 || got[0] != "s3:PutBucketTagging" || got[1] != "s3:DeleteBucket" {
		t.Errorf("MissingActions() = %v", got)
	}
}
