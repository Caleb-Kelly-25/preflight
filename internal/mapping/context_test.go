package mapping_test

import (
	"testing"
	"testing/fstest"

	"github.com/Caleb-Kelly-25/preflight/internal/finding"
	"github.com/Caleb-Kelly-25/preflight/internal/mapping"
)

func loadOne(t *testing.T, doc string) mapping.Resource {
	t.Helper()
	db, err := mapping.Load(fstest.MapFS{"t.yaml": &fstest.MapFile{Data: []byte(doc)}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	types := db.Types()
	if len(types) != 1 {
		t.Fatalf("expected one resource, got %v", types)
	}
	r, _ := db.Lookup(types[0])
	return r
}

const tagDoc = `
resources:
  - type: aws_thing
    service: s3
    status: draft
    context_keys:
      "aws:RequestTag/*": { from: tags }
      "s3:x-amz-acl":     { from: acl }
      "s3:max-keys":      { from: max_keys, type: numeric }
      "aws:SecureTransport": { from: secure, type: boolean }
    operations:
      create: [s3:CreateBucket]
`

func TestContextValueGlob(t *testing.T) {
	r := loadOne(t, tagDoc)
	attrs := map[string]any{"tags": map[string]any{"Environment": "prod"}}

	values, typ, ok := r.ContextValue("aws:RequestTag/Environment", attrs, nil)
	if !ok {
		t.Fatal("glob key was not sourced")
	}
	if len(values) != 1 || values[0] != "prod" {
		t.Errorf("values = %v, want [prod]", values)
	}
	if typ != finding.ContextString {
		t.Errorf("type = %q, want the string default", typ)
	}
}

func TestContextValueGlobMissingSubkey(t *testing.T) {
	r := loadOne(t, tagDoc)
	attrs := map[string]any{"tags": map[string]any{"Owner": "team"}}

	// The tag exists but not the one the policy asked about. Not sourceable —
	// and inventing an empty value would produce a confident wrong answer.
	if _, _, ok := r.ContextValue("aws:RequestTag/Environment", attrs, nil); ok {
		t.Error("sourced a value for a tag that is not set")
	}
}

func TestContextValueTypes(t *testing.T) {
	r := loadOne(t, tagDoc)
	attrs := map[string]any{
		"acl":      "private",
		"max_keys": float64(100), // plan JSON numbers decode as float64
		"secure":   true,
	}

	tests := map[string]struct {
		key   string
		value string
		typ   finding.ContextValueType
	}{
		"string":  {"s3:x-amz-acl", "private", finding.ContextString},
		"numeric": {"s3:max-keys", "100", finding.ContextNumeric},
		"boolean": {"aws:SecureTransport", "true", finding.ContextBoolean},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			values, typ, ok := r.ContextValue(tt.key, attrs, nil)
			if !ok {
				t.Fatalf("%s was not sourced", tt.key)
			}
			if len(values) != 1 || values[0] != tt.value {
				t.Errorf("values = %v, want [%s]", values, tt.value)
			}
			if typ != tt.typ {
				t.Errorf("type = %q, want %q", typ, tt.typ)
			}
		})
	}
}

// TestContextValueUnknownUntilApply is the case that matters most: an attribute
// Terraform cannot resolve until apply must never be guessed at.
func TestContextValueUnknownUntilApply(t *testing.T) {
	r := loadOne(t, tagDoc)
	attrs := map[string]any{"acl": nil}
	unknown := map[string]any{"acl": true}

	if _, _, ok := r.ContextValue("s3:x-amz-acl", attrs, unknown); ok {
		t.Error("sourced a value from an attribute that is unknown until apply")
	}
}

func TestContextValueNotDeclared(t *testing.T) {
	r := loadOne(t, tagDoc)
	attrs := map[string]any{"tags": map[string]any{"Environment": "prod"}}

	if _, _, ok := r.ContextValue("aws:SourceIp", attrs, nil); ok {
		t.Error("sourced a key the mapping never declared")
	}
}

func TestContextValueKeyMatchingIsCaseInsensitive(t *testing.T) {
	// IAM matches condition key names case-insensitively.
	r := loadOne(t, tagDoc)
	attrs := map[string]any{"acl": "private"}

	if _, _, ok := r.ContextValue("S3:X-Amz-Acl", attrs, nil); !ok {
		t.Error("case-insensitive key match failed")
	}
}

func TestContextValueList(t *testing.T) {
	doc := `
resources:
  - type: aws_thing
    service: s3
    status: draft
    context_keys:
      "aws:TagKeys": { from: tag_keys, type: stringList }
    operations:
      create: [s3:CreateBucket]
`
	r := loadOne(t, doc)
	attrs := map[string]any{"tag_keys": []any{"Owner", "Environment"}}

	values, typ, ok := r.ContextValue("aws:TagKeys", attrs, nil)
	if !ok {
		t.Fatal("list value was not sourced")
	}
	// Sorted, so the batch fingerprint is stable across runs.
	if len(values) != 2 || values[0] != "Environment" || values[1] != "Owner" {
		t.Errorf("values = %v, want [Environment Owner]", values)
	}
	if typ != finding.ContextStringList {
		t.Errorf("type = %q", typ)
	}
}

func TestContextKeysValidation(t *testing.T) {
	bad := map[string]string{
		"no from": `
resources:
  - type: aws_thing
    service: s3
    status: draft
    context_keys: { "aws:RequestTag/Env": {} }
    operations: { create: [s3:CreateBucket] }`,
		"unknown type": `
resources:
  - type: aws_thing
    service: s3
    status: draft
    context_keys: { "aws:RequestTag/Env": { from: tags, type: colour } }
    operations: { create: [s3:CreateBucket] }`,
		"not a condition key": `
resources:
  - type: aws_thing
    service: s3
    status: draft
    context_keys: { "RequestTag": { from: tags } }
    operations: { create: [s3:CreateBucket] }`,
		"non-trailing wildcard": `
resources:
  - type: aws_thing
    service: s3
    status: draft
    context_keys: { "aws:*Tag/Env": { from: tags } }
    operations: { create: [s3:CreateBucket] }`,
	}

	for name, doc := range bad {
		t.Run(name, func(t *testing.T) {
			_, err := mapping.Load(fstest.MapFS{"bad.yaml": &fstest.MapFile{Data: []byte(doc)}})
			if err == nil {
				t.Error("Load accepted an invalid context_keys declaration")
			}
		})
	}
}
