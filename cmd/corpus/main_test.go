package main

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/Caleb-Kelly-25/preflight/internal/mapping"
)

// A database with one entry per outcome the corpus is supposed to distinguish,
// so that every bucket in the report has a case whose answer is known by
// construction rather than by reading the tool's own output.
const testDB = `
resources:
  - type: aws_exactverified
    service: sns
    status: draft
    source: https://example.invalid/x
    verified_operations: [create]
    arn_format: "arn:${Partition}:sns:${Region}:${Account}:${Name}"
    arn_attributes:
      Name: name
    operations:
      create:
        - sns:CreateTopic
      delete:
        - sns:DeleteTopic

  - type: aws_exactdraft
    service: sns
    status: draft
    source: https://example.invalid/x
    arn_format: "arn:${Partition}:sns:${Region}:${Account}:${Name}"
    arn_attributes:
      Name: name
    operations:
      create:
        - sns:CreateTopic

  - type: aws_prefixed
    service: sns
    status: draft
    source: https://example.invalid/x
    arn_format: "arn:${Partition}:sns:${Region}:${Account}:${Name}"
    arn_attributes:
      Name: name
    arn_prefix_attributes:
      Name: name_prefix
    operations:
      create:
        - sns:CreateTopic

  - type: aws_wild
    service: sns
    status: draft
    source: https://example.invalid/x
    arn_format: "arn:${Partition}:sns:${Region}:${Account}:${Name}"
    arn_attributes:
      Name: name
    operations:
      create:
        - sns:CreateTopic
`

// Each change is one bucket. The delete is the load-bearing one: it is the only
// case that proves the corpus reads `before` rather than `after`, which is the
// single rule it duplicates from engine.prepare.
const testPlan = `{
  "format_version": "1.2",
  "terraform_version": "1.14.8",
  "resource_changes": [
    {
      "address": "aws_exactverified.a",
      "mode": "managed",
      "type": "aws_exactverified",
      "name": "a",
      "provider_name": "registry.terraform.io/hashicorp/aws",
      "change": { "actions": ["create"], "after": { "name": "known" }, "after_unknown": {} }
    },
    {
      "address": "aws_exactverified.gone",
      "mode": "managed",
      "type": "aws_exactverified",
      "name": "gone",
      "provider_name": "registry.terraform.io/hashicorp/aws",
      "change": { "actions": ["delete"], "before": { "name": "was-here" }, "after": null }
    },
    {
      "address": "aws_exactdraft.b",
      "mode": "managed",
      "type": "aws_exactdraft",
      "name": "b",
      "provider_name": "registry.terraform.io/hashicorp/aws",
      "change": { "actions": ["create"], "after": { "name": "known" }, "after_unknown": {} }
    },
    {
      "address": "aws_prefixed.c",
      "mode": "managed",
      "type": "aws_prefixed",
      "name": "c",
      "provider_name": "registry.terraform.io/hashicorp/aws",
      "change": { "actions": ["create"], "after": { "name_prefix": "pre-" }, "after_unknown": { "name": true } }
    },
    {
      "address": "aws_wild.d",
      "mode": "managed",
      "type": "aws_wild",
      "name": "d",
      "provider_name": "registry.terraform.io/hashicorp/aws",
      "change": { "actions": ["create"], "after": {}, "after_unknown": { "name": true } }
    },
    {
      "address": "aws_notmapped.e",
      "mode": "managed",
      "type": "aws_notmapped",
      "name": "e",
      "provider_name": "registry.terraform.io/hashicorp/aws",
      "change": { "actions": ["create"], "after": { "name": "known" }, "after_unknown": {} }
    },
    {
      "address": "google_storage_bucket.ignored",
      "mode": "managed",
      "type": "google_storage_bucket",
      "name": "ignored",
      "provider_name": "registry.terraform.io/hashicorp/google",
      "change": { "actions": ["create"], "after": { "name": "known" }, "after_unknown": {} }
    },
    {
      "address": "data.aws_caller_identity.ignored",
      "mode": "data",
      "type": "aws_caller_identity",
      "name": "ignored",
      "provider_name": "registry.terraform.io/hashicorp/aws",
      "change": { "actions": ["read"], "after": {}, "after_unknown": {} }
    },
    {
      "address": "aws_exactverified.untouched",
      "mode": "managed",
      "type": "aws_exactverified",
      "name": "untouched",
      "provider_name": "registry.terraform.io/hashicorp/aws",
      "change": { "actions": ["no-op"], "after": { "name": "known" }, "after_unknown": {} }
    }
  ]
}`

func loadTestDB(t *testing.T) *mapping.Database {
	t.Helper()
	db, err := mapping.Load(fstest.MapFS{"t.yaml": &fstest.MapFile{Data: []byte(testDB)}})
	if err != nil {
		t.Fatalf("loading test database: %v", err)
	}
	return db
}

func writeTestPlan(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(path, []byte(testPlan), 0o600); err != nil {
		t.Fatalf("writing test plan: %v", err)
	}
	return path
}

func TestMeasureOneBucketsEveryOutcome(t *testing.T) {
	one, err := measureOne(loadTestDB(t), writeTestPlan(t))
	if err != nil {
		t.Fatalf("measureOne: %v", err)
	}
	got, unmapped, wild, prefixed := &one.plan, one.unmapped, one.wildcard, one.prefixed

	// Six actionable units: the non-AWS resource, the data source and the no-op
	// must all be excluded, or the denominator of every percentage is wrong.
	want := counts{
		Units:       6,
		Verified:    1, // exact ARN + create is a verified operation
		DraftOp:     2, // aws_exactdraft create, and the delete of an entry verified only for create
		Inexact:     2, // prefix-derived and wildcard both cap at Likely
		Unmapped:    1,
		ARNExact:    3, // two creates with a known name, plus the delete read from `before`
		ARNPrefix:   1,
		ARNWildcard: 1,
	}
	if got.counts != want {
		t.Errorf("counts mismatch\n got %+v\nwant %+v", got.counts, want)
	}

	// The buckets must partition the units, or the report's percentages sum to
	// something other than 100 and nobody notices.
	if sum := got.Verified + got.DraftOp + got.Inexact + got.Unmapped; sum != got.Units {
		t.Errorf("confidence buckets sum to %d, want %d", sum, got.Units)
	}
	// ARN buckets cover the MAPPED units only; an unmapped type has no template.
	if sum := got.ARNExact + got.ARNPrefix + got.ARNWildcard; sum != got.Units-got.Unmapped {
		t.Errorf("arn buckets sum to %d, want %d", sum, got.Units-got.Unmapped)
	}

	if unmapped["aws_notmapped"] != 1 {
		t.Errorf("aws_notmapped should be counted once, got %d", unmapped["aws_notmapped"])
	}
	if len(unmapped) != 1 {
		t.Errorf("only aws_notmapped is unmapped, got %v", unmapped)
	}
	// The split that aws_iam_role exposed: a prefix-derived ARN is working as
	// designed and must not appear beside a bare wildcard, which has no signal
	// at all. Conflating them pointed improvement work at the wrong entries.
	if wild["aws_wild"] != 1 || len(wild) != 1 {
		t.Errorf("only aws_wild fell to a bare wildcard, got %v", wild)
	}
	if prefixed["aws_prefixed"] != 1 || len(prefixed) != 1 {
		t.Errorf("only aws_prefixed resolved via name_prefix, got %v", prefixed)
	}
}

// The delete case deserves its own assertion, because getting it wrong is silent:
// reading `after` on a delete yields nil attributes, every ARN degrades to "*",
// and the corpus would under-report exactness with no symptom at all.
func TestDeleteIsMeasuredAgainstPriorState(t *testing.T) {
	const deleteOnly = `{
  "format_version": "1.2",
  "resource_changes": [
    {
      "address": "aws_exactverified.gone",
      "mode": "managed",
      "type": "aws_exactverified",
      "name": "gone",
      "provider_name": "registry.terraform.io/hashicorp/aws",
      "change": { "actions": ["delete"], "before": { "name": "was-here" }, "after": null }
    }
  ]
}`
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(path, []byte(deleteOnly), 0o600); err != nil {
		t.Fatalf("writing plan: %v", err)
	}

	one, err := measureOne(loadTestDB(t), path)
	if err != nil {
		t.Fatalf("measureOne: %v", err)
	}
	got := &one.plan
	if got.ARNExact != 1 {
		t.Fatalf("a delete carrying its name in `before` must resolve exactly; got %+v", got.counts)
	}
}

// A corpus that quietly measured a subset would produce a confident number about
// the wrong population, so a file that is not a plan has to be an error the
// caller can see rather than an empty result.
func TestUnparseableFileIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notaplan.json")
	if err := os.WriteFile(path, []byte("this is not json"), 0o600); err != nil {
		t.Fatalf("writing file: %v", err)
	}
	if _, err := measureOne(loadTestDB(t), path); err == nil {
		t.Fatal("expected an error for a file that is not a plan")
	}
}

func TestCollectPlanFilesTakesNamedFilesWhateverTheExtension(t *testing.T) {
	dir := t.TempDir()
	named := filepath.Join(dir, "tfplan.out")
	nested := filepath.Join(dir, "sub", "a.json")
	ignored := filepath.Join(dir, "sub", "notes.txt")
	if err := os.MkdirAll(filepath.Dir(nested), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{named, nested, ignored} {
		if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	got, err := collectPlanFiles([]string{named, filepath.Join(dir, "sub")})
	if err != nil {
		t.Fatalf("collectPlanFiles: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want the named file and the nested .json, got %v", got)
	}
}

// The per-operation split exists to keep the headline honest, which only works
// if it is the same measurement partitioned. If the two ever disagree, the
// report shows a create rate that does not add up to the total it sits under.
func TestPerOperationCountsSumToTheTotal(t *testing.T) {
	one, err := measureOne(loadTestDB(t), writeTestPlan(t))
	if err != nil {
		t.Fatalf("measureOne: %v", err)
	}

	var sum counts
	for _, c := range one.byOp {
		sum.add(c)
	}
	if sum != one.plan.counts {
		t.Errorf("per-operation counts do not sum to the plan total\n got %+v\nwant %+v", sum, one.plan.counts)
	}

	// And the split must actually be a split: this plan has both creates and a
	// delete, so a single bucket would mean operations were not distinguished.
	if len(one.byOp) != 2 {
		t.Errorf("want create and delete buckets, got %d: %v", len(one.byOp), one.byOp)
	}
}
