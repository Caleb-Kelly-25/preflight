package mapping_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Caleb-Kelly-25/preflight/internal/mapping"
	"github.com/Caleb-Kelly-25/preflight/mappings"
)

// evidenceDir is read from disk rather than an embed, because evidence files are
// a maintainer and CI artifact and have no business in the shipped binary.
func evidenceDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join("..", "..", "mappings", "evidence")
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("locating the evidence directory: %v", err)
	}
	return dir
}

// TestVerifiedEntriesHaveEvidence is the guard on the strongest claim the
// database makes.
//
// `status: verified` and `verified_operations` are what let a finding reach
// `Verified` instead of being capped at `Likely`. Before this test, every such
// claim was backed only by prose in a `notes` field — 12 claims across 11
// entries, none of them checkable. Two failures were therefore invisible:
// claiming verification with no measurement behind it, and DELETING a measured
// action from a verified entry, which silently turns a proven claim into a false
// pass.
//
// This test closes both. It also makes aws_s3_bucket's claim — the oldest in the
// database, established by hand before the harness existed — auditable for the
// first time.
func TestVerifiedEntriesHaveEvidence(t *testing.T) {
	db, err := mapping.Load(mappings.FS)
	if err != nil {
		t.Fatalf("loading the shipped database: %v", err)
	}
	evidence, err := mapping.LoadEvidence(os.DirFS(evidenceDir(t)))
	if err != nil {
		t.Fatalf("loading evidence: %v", err)
	}

	for _, problem := range db.CheckEvidence(evidence) {
		t.Error(problem)
	}
}

// TestEvidenceCatchesADroppedAction is the test for the test. It demonstrates the
// exact edit TestVerifiedEntriesHaveEvidence exists to catch — removing a proven
// action from a verified entry — and would itself fail if CheckEvidence ever
// stopped noticing.
//
// Without this, a CheckEvidence that returned nothing at all would look perfectly
// healthy.
func TestEvidenceCatchesADroppedAction(t *testing.T) {
	// A verified create that needs two actions, one of which someone has removed
	// from the entry while the evidence still records it.
	db, err := mapping.Load(fstest.MapFS{"t.yaml": &fstest.MapFile{Data: []byte(`
resources:
  - type: aws_probe
    service: iam
    status: draft
    source: https://example.invalid/x
    verified_operations: [create]
    operations:
      create:
        - iam:CreatePolicy
`)}})
	if err != nil {
		t.Fatalf("loading database: %v", err)
	}

	evidence := map[string]mapping.Evidence{
		"aws_probe": {
			ResourceType: "aws_probe",
			Runs: []mapping.Run{{
				Operation:       mapping.OpCreate,
				Fixture:         "derivefixtures/aws_probe",
				DerivedAt:       "2026-09-26",
				ProviderVersion: "6.63.0",
				Sufficiency:     mapping.SufficiencyProven,
				Minimality:      "proven",
				// iam:GetPolicy was measured and is no longer in the entry.
				Actions: []string{"iam:CreatePolicy", "iam:GetPolicy"},
			}},
		},
	}

	problems := db.CheckEvidence(evidence)
	if len(problems) == 0 {
		t.Fatal("dropping a proven action from a verified entry was not reported")
	}
	if !anyContains(problems, "iam:GetPolicy") {
		t.Errorf("the report does not name the dropped action: %v", problems)
	}
}

// TestEvidenceRequiresARunForEveryVerifiedOperation covers the other direction:
// typing a verification claim that nothing measured.
func TestEvidenceRequiresARunForEveryVerifiedOperation(t *testing.T) {
	db, err := mapping.Load(fstest.MapFS{"t.yaml": &fstest.MapFile{Data: []byte(`
resources:
  - type: aws_probe
    service: iam
    status: draft
    source: https://example.invalid/x
    verified_operations: [create, delete]
    operations:
      create:
        - iam:CreatePolicy
      delete:
        - iam:DeletePolicy
`)}})
	if err != nil {
		t.Fatalf("loading database: %v", err)
	}

	run := mapping.Run{
		Operation:       mapping.OpCreate,
		Fixture:         "derivefixtures/aws_probe",
		DerivedAt:       "2026-09-26",
		ProviderVersion: "6.63.0",
		Sufficiency:     mapping.SufficiencyProven,
		Minimality:      "proven",
		Actions:         []string{"iam:CreatePolicy"},
	}

	// delete is claimed and has no run at all.
	problems := db.CheckEvidence(map[string]mapping.Evidence{
		"aws_probe": {ResourceType: "aws_probe", Runs: []mapping.Run{run}},
	})
	if !anyContains(problems, "delete") {
		t.Errorf("a verified operation with no evidence run was not reported: %v", problems)
	}

	// No evidence file whatsoever.
	if problems := db.CheckEvidence(nil); len(problems) == 0 {
		t.Error("a verified entry with no evidence file at all was not reported")
	}

	// A run that did not prove sufficiency cannot support the claim: the claim IS
	// that the mapped set is sufficient.
	weak := run
	weak.Sufficiency = "inconclusive"
	problems = db.CheckEvidence(map[string]mapping.Evidence{
		"aws_probe": {ResourceType: "aws_probe", Runs: []mapping.Run{weak}},
	})
	if !anyContains(problems, "sufficiency") {
		t.Errorf("a run with unproven sufficiency was accepted for a verified operation: %v", problems)
	}
}

// TestEvidenceAllowsTheEntryToHoldMore pins the asymmetry, which is the part most
// likely to be "tidied up" into an equality check by someone who has not hit the
// case it protects.
//
// An entry legitimately retains actions no fixture could exercise.
// iam:DeletePolicyVersion is the worked example: minimisation proved it droppable
// for a one-version update, and it is genuinely required once a policy reaches
// AWS's five-version cap, which a fixture applying its change once cannot reach.
// Demanding equality would force that action out of the entry and ship the false
// pass the measurement appeared to justify.
func TestEvidenceAllowsTheEntryToHoldMore(t *testing.T) {
	db, err := mapping.Load(fstest.MapFS{"t.yaml": &fstest.MapFile{Data: []byte(`
resources:
  - type: aws_probe
    service: iam
    status: draft
    source: https://example.invalid/x
    verified_operations: [update]
    operations:
      update:
        - iam:CreatePolicyVersion
        - iam:DeletePolicyVersion
        - action: iam:TagPolicy
          when: { attribute_changed: [tags, tags_all] }
`)}})
	if err != nil {
		t.Fatalf("loading database: %v", err)
	}

	// The run measured neither the retained surplus nor the gated tagging action.
	problems := db.CheckEvidence(map[string]mapping.Evidence{
		"aws_probe": {
			ResourceType: "aws_probe",
			Runs: []mapping.Run{{
				Operation:       mapping.OpUpdate,
				Fixture:         "derivefixtures/aws_probe__update",
				DerivedAt:       "2026-09-26",
				ProviderVersion: "6.63.0",
				Sufficiency:     mapping.SufficiencyProven,
				Minimality:      "proven",
				Actions:         []string{"iam:CreatePolicyVersion"},
			}},
		},
	})
	if len(problems) != 0 {
		t.Errorf("an entry holding more than a run measured was reported: %v", problems)
	}
}

// TestEvidenceForAnUnknownTypeIsReported catches a rename that left its evidence
// behind, which would otherwise look like proof of something that no longer
// exists.
func TestEvidenceForAnUnknownTypeIsReported(t *testing.T) {
	db, err := mapping.Load(fstest.MapFS{"t.yaml": &fstest.MapFile{Data: []byte(`
resources:
  - type: aws_probe
    service: iam
    status: draft
    operations:
      create: [iam:CreatePolicy]
`)}})
	if err != nil {
		t.Fatalf("loading database: %v", err)
	}
	problems := db.CheckEvidence(map[string]mapping.Evidence{
		"aws_renamed_away": {ResourceType: "aws_renamed_away"},
	})
	if !anyContains(problems, "aws_renamed_away") {
		t.Errorf("orphaned evidence was not reported: %v", problems)
	}
}

// TestEvidenceFilenameMustMatchResourceType stops an evidence file being silently
// ignored because it is filed under the wrong name — which would look exactly
// like a missing measurement, or worse, like a present one.
func TestEvidenceFilenameMustMatchResourceType(t *testing.T) {
	_, err := mapping.LoadEvidence(fstest.MapFS{
		"aws_wrong_name.json": &fstest.MapFile{Data: []byte(`{"resource_type":"aws_probe","runs":[]}`)},
	})
	if err == nil {
		t.Error("LoadEvidence accepted a file whose name does not match its resource_type")
	}
}

func anyContains(problems []string, want string) bool {
	for _, p := range problems {
		if strings.Contains(p, want) {
			return true
		}
	}
	return false
}
