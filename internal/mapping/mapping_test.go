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
	if len(actions) != 1 || actions[0].Action != "example:CreateThing" {
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
		// Partial verification is still a verification claim, so it carries the
		// same requirement to say where it came from.
		"verified_operations without source": `
resources:
  - type: aws_thing
    service: s3
    status: draft
    verified_operations: [create]
    operations: {create: [s3:CreateBucket]}`,
		"verified_operations alongside status verified": `
resources:
  - type: aws_thing
    service: s3
    status: verified
    source: https://example.invalid/x
    verified_operations: [create]
    operations: {create: [s3:CreateBucket]}`,
		// arn_or_name decides whether a value is safe to template. A
		// declaration missing either half cannot tell `role/x` from `user/x`,
		// or a lambda ARN from an S3 one, so it would admit exactly the
		// confidently-wrong ARN it exists to exclude.
		"arn_or_name without service": `
resources:
  - type: aws_thing
    service: lambda
    status: draft
    arn_format: "arn:${Partition}:lambda:${Region}:${Account}:function:${Name}"
    arn_attributes: {Name: function_name}
    arn_or_name: {Name: {resource_type: function}}
    operations: {create: [lambda:AddPermission]}`,
		"arn_or_name without resource_type": `
resources:
  - type: aws_thing
    service: lambda
    status: draft
    arn_format: "arn:${Partition}:lambda:${Region}:${Account}:function:${Name}"
    arn_attributes: {Name: function_name}
    arn_or_name: {Name: {service: lambda}}
    operations: {create: [lambda:AddPermission]}`,
		// Keyed to a variable that does not exist, it would silently never run
		// and the polymorphic value would be templated unchecked.
		"arn_or_name names an unused variable": `
resources:
  - type: aws_thing
    service: lambda
    status: draft
    arn_format: "arn:${Partition}:lambda:${Region}:${Account}:function:${Name}"
    arn_attributes: {Name: function_name}
    arn_or_name: {Other: {service: lambda, resource_type: function}}
    operations: {create: [lambda:AddPermission]}`,
		"arn_or_name with no attribute to read": `
resources:
  - type: aws_thing
    service: lambda
    status: draft
    arn_format: "arn:${Partition}:lambda:${Region}:${Account}:function:${Name}"
    arn_prefix_attributes: {Name: name_prefix}
    arn_or_name: {Name: {service: lambda, resource_type: function}}
    operations: {create: [lambda:AddPermission]}`,
		// Without a template there is nothing to build from the bare-name half.
		"reference arn_or_name without arn_format": `
resources:
  - type: aws_thing
    service: lambda
    status: draft
    references:
      - action: iam:PassRole
        arn_from: role
        arn_or_name: {service: iam, resource_type: role}
    operations: {create: [lambda:CreateFunction]}`,
		// Claiming an unmapped operation is proven is a claim about nothing.
		"verified_operations names an unmapped operation": `
resources:
  - type: aws_thing
    service: s3
    status: draft
    source: https://example.invalid/x
    verified_operations: [delete]
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

// crossServiceActions are the actions a resource genuinely needs from a service
// other than its own. Each one is an exception to the service-prefix rule below
// and needs a reason here, because the rule catches a real class of typo and a
// silent exception would blunt it.
//
// This is a stopgap for a schema gap rather than a design. A service-linked role
// requirement is not a property of the resource being created — it is an
// account-level, once-only precondition — and `references`, the schema's
// cross-resource mechanism, cannot express it because there is no attribute on
// the resource holding the role's ARN.
var crossServiceActions = map[string]bool{
	// ELB creates its service-linked role on the FIRST load balancer in an
	// account, and the caller must hold this for that to succeed. Over-reports
	// for every account that already has the role. See mappings/elb.yaml. RDS
	// needs the same action for a DB subnet group, measured 2026-09-26.
	"iam:CreateServiceLinkedRole": true,

	// MEASURED, not assumed. Creating an aws_lb_target_group needs both: ELB
	// validates the VPC the target group names, and checks whether that VPC has an
	// internet gateway to decide which target and address types are eligible.
	// Neither was in the entry, and neither is reachable by reasoning about load
	// balancing. See mappings/elb.yaml.
	"ec2:DescribeVpcs":             true,
	"ec2:DescribeInternetGateways": true,
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
		//
		// The allowlist is deliberately a list of exact action NAMES, not of
		// prefixes. Allowing a prefix like "iam:" anywhere would let
		// "elbv2:CreateListener" through the day someone declares an entry's
		// service as something else, and catching that typo is the whole reason
		// this check exists.
		for op, actions := range r.Operations {
			for _, a := range actions {
				if crossServiceActions[a.Action] {
					continue
				}
				prefix, _, _ := strings.Cut(a.Action, ":")
				if prefix != r.Service {
					t.Errorf("%s %s: action %q does not use the declared service prefix %q "+
						"(add it to crossServiceActions only if it is genuinely a cross-service "+
						"requirement, with the reason)",
						typ, op, a.Action, r.Service)
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

// TestShippedPolymorphicARNs pins the two entries that lost ARN scoping to the
// name-or-ARN ambiguity and got it back through `arn_or_name`.
//
// Both spellings have to land on the same ARN. If they do not, the check a
// user gets depends on how their configuration happened to be written, which is
// worse than the wildcard these entries used to fall back to.
func TestShippedPolymorphicARNs(t *testing.T) {
	db, err := mapping.Load(mappings.FS)
	if err != nil {
		t.Fatalf("the shipped mapping database does not load: %v", err)
	}
	ctx := mapping.ARNContext{Partition: "aws", Account: "123456789012", Region: "us-east-1"}

	tests := []struct {
		typ   string
		attrs []map[string]any
		want  string
	}{
		{
			typ: "aws_lambda_permission",
			attrs: []map[string]any{
				{"function_name": "my-function"},
				{"function_name": "arn:aws:lambda:us-east-1:123456789012:function:my-function"},
			},
			want: "arn:aws:lambda:us-east-1:123456789012:function:my-function",
		},
		{
			typ: "aws_ecs_service",
			attrs: []map[string]any{
				{"cluster": "prod", "name": "api"},
				{"cluster": "arn:aws:ecs:us-east-1:123456789012:cluster/prod", "name": "api"},
			},
			want: "arn:aws:ecs:us-east-1:123456789012:service/prod/api",
		},
	}

	for _, tc := range tests {
		t.Run(tc.typ, func(t *testing.T) {
			r, ok := db.Lookup(tc.typ)
			if !ok {
				t.Fatalf("%s is not in the shipped database", tc.typ)
			}
			for _, attrs := range tc.attrs {
				got, exact := r.BuildARN(ctx, attrs, nil)
				if !exact {
					t.Errorf("%v: exact = false, want true", attrs)
				}
				if got != tc.want {
					t.Errorf("%v: got %q, want %q", attrs, got, tc.want)
				}
			}

			// And the safety half: a value that is neither spelling must not be
			// templated into a confident wrong ARN.
			bad := map[string]any{"function_name": "123456789012:function:f", "cluster": "some:thing", "name": "api"}
			if got, exact := r.BuildARN(ctx, bad, nil); exact || got != "*" {
				t.Errorf("unidentifiable value: got (%q, %v), want (%q, false)", got, exact, "*")
			}
		})
	}
}
