package mapping_test

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Caleb-Kelly-25/preflight/internal/mapping"
	"github.com/Caleb-Kelly-25/preflight/mappings"
)

// TestTagGatesAlsoNameTagsAll is the durable guard for a false pass that shipped
// in 51 gates across 11 services.
//
// Provider-level `default_tags` merge into each resource's COMPUTED `tags_all`,
// never into its `tags`. So a plan that changes only `default_tags` shows
// `tags_all` changed and `tags` untouched, and a gate reading `tags` alone drops
// the tagging action — reporting a plan as safe that cannot apply.
//
// Measured 2026-09-26 against real AWS: a `default_tags`-only change to an
// otherwise untouched aws_iam_policy requires iam:TagPolicy. See
// derivefixtures/aws_iam_policy__update_default_tags.
//
// The rule this enforces is mechanical, which is the point — the defect was not
// a reasoning error in one entry, it was one pattern copied 51 times, and the
// fix only holds if the 52nd copy fails CI.
func TestTagGatesAlsoNameTagsAll(t *testing.T) {
	db, err := mapping.Load(mappings.FS)
	if err != nil {
		t.Fatalf("loading the shipped database: %v", err)
	}

	for _, typ := range db.Types() {
		res, _ := db.Lookup(typ)
		for op, actions := range res.Operations {
			for _, a := range actions {
				if a.When == nil {
					continue
				}
				check := func(kind string, names mapping.AttrNames) {
					if !contains(names, "tags") {
						return
					}
					if !contains(names, "tags_all") {
						t.Errorf("%s %s: %s gates %s on %v without tags_all — a default_tags-only "+
							"change would drop this action and produce a false pass",
							res.Type, op, kind, a.Action, []string(names))
					}
				}
				check("attribute_set", a.When.AttributeSet)
				check("attribute_changed", a.When.AttributeChanged)
			}
		}
	}
}

// TestTagsAllGateCatchesDefaultTagsOnlyChange exercises the engine-facing
// behaviour the corrected gates rely on, at both ends: the corrected form fires
// when only tags_all moved, and the old form does not. The second half is what
// makes this a regression test rather than a tautology — it demonstrates the bug
// the fix removes.
func TestTagsAllGateCatchesDefaultTagsOnlyChange(t *testing.T) {
	// A default_tags change: the resource's own tags are identical, and only the
	// merged set differs. This is the exact shape terraform emits.
	before := map[string]any{
		"tags":     map[string]any{"env": "derive"},
		"tags_all": map[string]any{"env": "derive"},
	}
	after := map[string]any{
		"tags":     map[string]any{"env": "derive"},
		"tags_all": map[string]any{"env": "derive", "added": "true"},
	}

	corrected := parseOne(t, `
resources:
  - type: aws_probe
    service: iam
    status: draft
    operations:
      update:
        - action: iam:TagPolicy
          when: { attribute_changed: [tags, tags_all] }
`)
	got := corrected.RequiredActions(mapping.OpUpdate, after, nil, before, after)
	if len(got) != 1 {
		t.Errorf("corrected gate: got %v, want [iam:TagPolicy] — a default_tags change needs the tagging action", got)
	}

	// The shape that shipped. Kept deliberately: if this ever starts passing,
	// either the plan representation changed or someone widened the semantics,
	// and both are worth knowing about.
	old := parseOne(t, `
resources:
  - type: aws_probe
    service: iam
    status: draft
    operations:
      update:
        - action: iam:TagPolicy
          when: { attribute_changed: [tags] }
`)
	if got := old.RequiredActions(mapping.OpUpdate, after, nil, before, after); len(got) != 0 {
		t.Errorf("the pre-fix gate returned %v; the bug being guarded against was that it returns nothing", got)
	}
}

// TestAttributeSetAcceptsBareStringAndList pins the backward compatibility that
// let 51 gates be corrected without rewriting every other entry: `attribute_set`
// takes one name or several, and one name is still spelled without brackets.
func TestAttributeSetAcceptsBareStringAndList(t *testing.T) {
	bare := parseOne(t, `
resources:
  - type: aws_probe
    service: iam
    status: draft
    operations:
      create:
        - action: iam:TagPolicy
          when: { attribute_set: tags }
`)
	list := parseOne(t, `
resources:
  - type: aws_probe
    service: iam
    status: draft
    operations:
      create:
        - action: iam:TagPolicy
          when: { attribute_set: [tags, tags_all] }
`)

	tagged := map[string]any{"tags": map[string]any{"env": "x"}}
	for name, res := range map[string]mapping.Resource{"bare": bare, "list": list} {
		if got := res.RequiredActions(mapping.OpCreate, tagged, nil, nil, tagged); len(got) != 1 {
			t.Errorf("%s form, tagged: got %v, want the action", name, got)
		}
	}

	// Any-of: the list form fires on tags_all alone, which is the whole reason
	// it exists. The bare form cannot.
	viaDefaults := map[string]any{"tags_all": map[string]any{"env": "x"}}
	if got := list.RequiredActions(mapping.OpCreate, viaDefaults, nil, nil, viaDefaults); len(got) != 1 {
		t.Errorf("list form, tags only via default_tags: got %v, want the action", got)
	}
	if got := bare.RequiredActions(mapping.OpCreate, viaDefaults, nil, nil, viaDefaults); len(got) != 0 {
		t.Errorf("bare form should not fire on tags_all; got %v", got)
	}

	// Neither fires when nothing is tagged at all — the gate still does its job.
	untagged := map[string]any{"tags": map[string]any{}, "tags_all": map[string]any{}}
	for name, res := range map[string]mapping.Resource{"bare": bare, "list": list} {
		if got := res.RequiredActions(mapping.OpCreate, untagged, nil, nil, untagged); len(got) != 0 {
			t.Errorf("%s form, untagged: got %v, want nothing", name, got)
		}
	}
}

func parseOne(t *testing.T, doc string) mapping.Resource {
	t.Helper()
	db, err := mapping.Load(fstest.MapFS{
		"probe.yaml": &fstest.MapFile{Data: []byte(doc)},
	})
	if err != nil {
		t.Fatalf("parsing fixture: %v", err)
	}
	if db.Len() != 1 {
		t.Fatalf("want exactly one resource, got %d", db.Len())
	}
	res, ok := db.Lookup("aws_probe")
	if !ok {
		t.Fatal("aws_probe not found in the fixture")
	}
	return res
}

func contains(names mapping.AttrNames, want string) bool {
	for _, n := range names {
		if strings.EqualFold(n, want) {
			return true
		}
	}
	return false
}
