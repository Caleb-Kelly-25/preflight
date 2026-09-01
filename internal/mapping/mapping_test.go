package mapping_test

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Caleb-Kelly-25/preflight/internal/mapping"
	"github.com/Caleb-Kelly-25/preflight/mappings"
)

func TestLoad(t *testing.T) {
	fsys := fstest.MapFS{
		"example.yaml": &fstest.MapFile{Data: []byte(`
resources:
  - type: aws_example
    service: example
    status: draft
    arn_format: "arn:${Partition}:example:::${Name}"
    arn_attributes:
      Name: name
    operations:
      create:
        - example:CreateThing
      delete:
        - example:DeleteThing
`)},
		"ignored.txt": &fstest.MapFile{Data: []byte("not yaml")},
	}

	db, err := mapping.Load(fsys)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if db.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", db.Len())
	}

	actions, ok := db.Actions("aws_example", mapping.OpCreate)
	if !ok {
		t.Fatal("Actions() reported the type as unmapped")
	}
	if len(actions) != 1 || actions[0] != "example:CreateThing" {
		t.Errorf("create actions = %v", actions)
	}

	// An unmapped type must be distinguishable from one with no actions, so the
	// caller can report Unchecked rather than a pass.
	if _, ok := db.Actions("aws_unmapped", mapping.OpCreate); ok {
		t.Error("Actions() reported an unmapped type as mapped")
	}
}

func TestLoadRejectsInvalid(t *testing.T) {
	tests := map[string]string{
		"missing type": `
resources:
  - service: s3
    status: draft
    operations: {create: [s3:CreateBucket]}`,
		"missing status": `
resources:
  - type: aws_thing
    service: s3
    operations: {create: [s3:CreateBucket]}`,
		"no operations": `
resources:
  - type: aws_thing
    service: s3
    status: draft
    operations: {}`,
		"unknown operation": `
resources:
  - type: aws_thing
    service: s3
    status: draft
    operations: {frobnicate: [s3:CreateBucket]}`,
		"malformed action": `
resources:
  - type: aws_thing
    service: s3
    status: draft
    operations: {create: [CreateBucket]}`,
		// `verified` is what lets a finding reach Verified instead of being
		// capped at Likely. Claiming it with no stated basis is exactly the
		// unverified-presenting-as-safe failure the tool exists to prevent.
		"verified without source": `
resources:
  - type: aws_thing
    service: s3
    status: verified
    operations: {create: [s3:CreateBucket]}`,
		"verified with blank source": `
resources:
  - type: aws_thing
    service: s3
    status: verified
    source: "   "
    operations: {create: [s3:CreateBucket]}`,
	}

	for name, doc := range tests {
		t.Run(name, func(t *testing.T) {
			fsys := fstest.MapFS{"bad.yaml": &fstest.MapFile{Data: []byte(doc)}}
			if _, err := mapping.Load(fsys); err == nil {
				t.Error("Load accepted an invalid mapping document")
			}
		})
	}
}

func TestLoadRejectsDuplicateType(t *testing.T) {
	doc := `
resources:
  - type: aws_thing
    service: s3
    status: draft
    operations: {create: [s3:CreateBucket]}
  - type: aws_thing
    service: s3
    status: draft
    operations: {delete: [s3:DeleteBucket]}`
	fsys := fstest.MapFS{"dup.yaml": &fstest.MapFile{Data: []byte(doc)}}
	if _, err := mapping.Load(fsys); err == nil {
		t.Error("Load accepted a duplicate resource type")
	}
}

// TestShippedDatabase guards the mapping files we actually ship. A broken entry
// here would silently reduce coverage in the field, which the confidence model
// exists to prevent.
func TestShippedDatabase(t *testing.T) {
	db, err := mapping.Load(mappings.FS)
	if err != nil {
		t.Fatalf("the shipped mapping database does not load: %v", err)
	}
	if db.Len() == 0 {
		t.Fatal("the shipped mapping database is empty")
	}

	for _, typ := range db.Types() {
		r, _ := db.Lookup(typ)

		if !strings.HasPrefix(typ, "aws_") {
			t.Errorf("%s: resource type should start with aws_", typ)
		}

		// Every ARN template variable needs an attribute to fill it from,
		// otherwise the ARN silently degrades to a wildcard on every run.
		for _, v := range r.ARNVars() {
			if _, ok := r.ARNAttributes[v]; !ok {
				t.Errorf("%s: arn_format uses ${%s} but arn_attributes has no entry for it", typ, v)
			}
		}

		// Actions must use their resource's own service prefix. A typo here
		// produces a permanently-denied simulation that looks like a real gap.
		for op, actions := range r.Operations {
			for _, a := range actions {
				prefix, _, _ := strings.Cut(a, ":")
				if prefix != r.Service {
					t.Errorf("%s %s: action %q does not use the declared service prefix %q",
						typ, op, a, r.Service)
				}
			}
		}

		// Condition keys are either global (aws:) or the resource's own
		// service. A key from an unrelated service can never be referenced by a
		// policy governing this resource, so it would silently never match.
		for key, src := range r.ContextKeys {
			prefix, _, _ := strings.Cut(key, ":")
			if prefix != "aws" && prefix != r.Service {
				t.Errorf("%s: context key %q uses neither the aws: prefix nor the declared service %q",
					typ, key, r.Service)
			}
			if src.From == "" {
				t.Errorf("%s: context key %q has no source attribute", typ, key)
			}
		}
	}
}
