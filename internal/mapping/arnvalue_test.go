package mapping

import "testing"

// TestARNOrNameClassify is the adversarial half of the feature. Detection is
// what stands between "this attribute might be an ARN" and a confidently wrong
// ARN, so the cases that matter most are the ones that are nearly an ARN.
func TestARNOrNameClassify(t *testing.T) {
	lambda := ARNOrName{Service: "lambda", ResourceType: "function"}

	tests := []struct {
		name  string
		decl  ARNOrName
		value string
		want  arnValueKind
		id    string
		why   string
	}{
		{
			name:  "bare name",
			decl:  lambda,
			value: "my-function",
			want:  valueName,
		},
		{
			name:  "full arn of the declared kind",
			decl:  lambda,
			value: "arn:aws:lambda:us-east-1:123456789012:function:my-function",
			want:  valueARN,
			id:    "my-function",
		},
		{
			name:  "slash-separated resource type",
			decl:  ARNOrName{Service: "ecs", ResourceType: "cluster"},
			value: "arn:aws:ecs:eu-west-1:123456789012:cluster/prod",
			want:  valueARN,
			id:    "prod",
		},
		{
			name:  "empty string",
			decl:  lambda,
			value: "",
			want:  valueAmbiguous,
			why:   "there is nothing to build from",
		},
		{
			// The case that makes a substring check dangerous. A colon alone
			// says nothing; this is a container tag, not an ARN, and templating
			// it would produce arn:...:function:myapp:v2.
			name:  "colon but not an arn",
			decl:  lambda,
			value: "myapp:v2",
			want:  valueAmbiguous,
			why:   "a colon does not make a value an ARN, and it does make it not a name",
		},
		{
			// THE INTERESTING CASE. This is a perfectly well-formed ARN. It is
			// rejected anyway: see classify's comment. Passing it through would
			// simulate a lambda action against an S3 ARN, which lambda.yaml
			// records as producing an implicit deny — a false positive
			// manufactured out of a modelling error.
			name:  "arn for the wrong service",
			decl:  lambda,
			value: "arn:aws:s3:::my-bucket",
			want:  valueAmbiguous,
			why:   "a well-formed ARN naming another service means the premise is broken, not that the value is usable",
		},
		{
			// Same reasoning one level down: role/x and user/x are the same
			// value without a resource-type check.
			name:  "arn for the wrong resource type",
			decl:  ARNOrName{Service: "iam", ResourceType: "role"},
			value: "arn:aws:iam::123456789012:user/deploy",
			want:  valueAmbiguous,
			why:   "building role/deploy out of a user ARN is a confident wrong answer",
		},
		{
			name:  "arn missing segments",
			decl:  lambda,
			value: "arn:aws:lambda:function:my-function",
			want:  valueAmbiguous,
			why:   "five fields, not six",
		},
		{
			name:  "arn with an empty resource portion",
			decl:  lambda,
			value: "arn:aws:lambda:us-east-1:123456789012:",
			want:  valueAmbiguous,
		},
		{
			name:  "arn prefix with nothing after it",
			decl:  lambda,
			value: "arn:",
			want:  valueAmbiguous,
		},
		{
			// A partial ARN, which the Lambda API genuinely accepts in
			// function_name. It is not an ARN by our parser, and it must NOT be
			// treated as a name either: that is what the three-valued result is
			// for.
			name:  "partial lambda arn",
			decl:  lambda,
			value: "123456789012:function:my-function",
			want:  valueAmbiguous,
			why:   "accepted by Lambda, but templating it yields function:123456789012:function:my-function",
		},
		{
			// An alias/version qualifier is a different ARN from the
			// unqualified function, so neither spelling can be trusted to match
			// the other.
			name:  "alias-qualified function arn",
			decl:  lambda,
			value: "arn:aws:lambda:us-east-1:123456789012:function:my-function:PROD",
			want:  valueAmbiguous,
			why:   "the qualified ARN is not the ARN a policy scoped to the function would match",
		},
		{
			name:  "resource half of an arn pasted as a name",
			decl:  ARNOrName{Service: "ecs", ResourceType: "cluster"},
			value: "cluster/prod",
			want:  valueAmbiguous,
			why:   "templating it would give service/cluster/prod/name",
		},
		{
			name:  "non-numeric account",
			decl:  lambda,
			value: "arn:aws:lambda:us-east-1:not-an-account:function:my-function",
			want:  valueAmbiguous,
			why:   "the account is adopted into the rebuilt ARN, so an unrecognised one is not used",
		},
		{
			name:  "govcloud partition",
			decl:  lambda,
			value: "arn:aws-us-gov:lambda:us-gov-west-1:123456789012:function:my-function",
			want:  valueARN,
			id:    "my-function",
		},
		{
			// The account field is legitimately "aws" for AWS-managed IAM
			// policies, and legitimately empty for global services.
			name:  "aws-managed policy arn",
			decl:  ARNOrName{Service: "iam", ResourceType: "policy"},
			value: "arn:aws:iam::aws:policy/AdministratorAccess",
			want:  valueARN,
			id:    "AdministratorAccess",
		},
		{
			name:  "arn-looking prefix on something else",
			decl:  lambda,
			value: "arnold:aws:lambda:us-east-1:123456789012:function:f",
			want:  valueAmbiguous,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, p := tc.decl.classify(tc.value)
			if got != tc.want {
				t.Fatalf("classify(%q) = %v, want %v (%s)", tc.value, got, tc.want, tc.why)
			}
			if tc.id != "" && p.ResourceID != tc.id {
				t.Errorf("resource id = %q, want %q", p.ResourceID, tc.id)
			}
		})
	}
}

// TestBuildARNOrName covers the resource half: an ARN variable whose attribute
// may hold either spelling.
func TestBuildARNOrName(t *testing.T) {
	perm := Resource{
		Type:          "aws_lambda_permission",
		Service:       "lambda",
		ARNFormat:     "arn:${Partition}:lambda:${Region}:${Account}:function:${FunctionName}",
		ARNAttributes: map[string]string{"FunctionName": "function_name"},
		ARNOrName: map[string]ARNOrName{
			"FunctionName": {Service: "lambda", ResourceType: "function"},
		},
	}
	ctx := ARNContext{Partition: "aws", Account: "123456789012", Region: "us-east-1"}
	want := "arn:aws:lambda:us-east-1:123456789012:function:my-function"

	t.Run("bare name is templated", func(t *testing.T) {
		got, exact := perm.BuildARN(ctx, map[string]any{"function_name": "my-function"}, nil)
		if !exact || got != want {
			t.Errorf("got (%q, %v), want (%q, true)", got, exact, want)
		}
	})

	// Both spellings must land on the same ARN, or the check depends on how the
	// configuration happened to be written.
	t.Run("full arn produces the same arn", func(t *testing.T) {
		got, exact := perm.BuildARN(ctx, map[string]any{"function_name": want}, nil)
		if !exact || got != want {
			t.Errorf("got (%q, %v), want (%q, true)", got, exact, want)
		}
	})

	// The region and account come from the value, not from whoever is running
	// terraform. Using the caller's here would build a well-formed ARN naming a
	// function that does not exist.
	t.Run("cross-region arn keeps its own region and account", func(t *testing.T) {
		v := "arn:aws:lambda:eu-west-1:999999999999:function:other"
		got, exact := perm.BuildARN(ctx, map[string]any{"function_name": v}, nil)
		if !exact || got != v {
			t.Errorf("got (%q, %v), want (%q, true)", got, exact, v)
		}
	})

	t.Run("unidentifiable value degrades to wildcard", func(t *testing.T) {
		for _, v := range []string{
			"123456789012:function:my-function",
			"arn:aws:s3:::my-bucket",
			"my-function:PROD",
		} {
			got, exact := perm.BuildARN(ctx, map[string]any{"function_name": v}, nil)
			if exact || got != "*" {
				t.Errorf("%q: got (%q, %v), want (%q, false)", v, got, exact, "*")
			}
		}
	})

	t.Run("unknown-until-apply still degrades to wildcard", func(t *testing.T) {
		got, exact := perm.BuildARN(ctx,
			map[string]any{"function_name": nil},
			map[string]any{"function_name": true})
		if exact || got != "*" {
			t.Errorf("got (%q, %v), want (%q, false)", got, exact, "*")
		}
	})
}

// TestBuildARNOrNameComponent covers the other shape the field has to serve:
// the polymorphic attribute is one COMPONENT of the target ARN, not the whole
// of it. An ECS service ARN embeds its cluster's name.
func TestBuildARNOrNameComponent(t *testing.T) {
	svc := Resource{
		Type:      "aws_ecs_service",
		Service:   "ecs",
		ARNFormat: "arn:${Partition}:ecs:${Region}:${Account}:service/${ClusterName}/${ServiceName}",
		ARNAttributes: map[string]string{
			"ClusterName": "cluster",
			"ServiceName": "name",
		},
		ARNOrName: map[string]ARNOrName{
			"ClusterName": {Service: "ecs", ResourceType: "cluster"},
		},
	}
	ctx := ARNContext{Partition: "aws", Account: "123456789012", Region: "us-east-1"}

	t.Run("cluster name", func(t *testing.T) {
		got, exact := svc.BuildARN(ctx, map[string]any{"cluster": "prod", "name": "api"}, nil)
		want := "arn:aws:ecs:us-east-1:123456789012:service/prod/api"
		if !exact || got != want {
			t.Errorf("got (%q, %v), want (%q, true)", got, exact, want)
		}
	})

	t.Run("cluster arn contributes its name", func(t *testing.T) {
		got, exact := svc.BuildARN(ctx, map[string]any{
			"cluster": "arn:aws:ecs:us-east-1:123456789012:cluster/prod",
			"name":    "api",
		}, nil)
		want := "arn:aws:ecs:us-east-1:123456789012:service/prod/api"
		if !exact || got != want {
			t.Errorf("got (%q, %v), want (%q, true)", got, exact, want)
		}
	})

	// The service follows its cluster, not the caller. Building this in the
	// caller's region would be the "confidently wrong" outcome the whole field
	// exists to avoid.
	t.Run("cluster arn in another account moves the service arn with it", func(t *testing.T) {
		got, exact := svc.BuildARN(ctx, map[string]any{
			"cluster": "arn:aws:ecs:eu-west-1:999999999999:cluster/prod",
			"name":    "api",
		}, nil)
		want := "arn:aws:ecs:eu-west-1:999999999999:service/prod/api"
		if !exact || got != want {
			t.Errorf("got (%q, %v), want (%q, true)", got, exact, want)
		}
	})

	t.Run("missing cluster degrades rather than assuming the default cluster", func(t *testing.T) {
		got, exact := svc.BuildARN(ctx, map[string]any{"name": "api"}, nil)
		if exact || got != "*" {
			t.Errorf("got (%q, %v), want (%q, false)", got, exact, "*")
		}
	})
}

// TestReferenceARNOrName covers the reference half. One declaration type, one
// detection function, two call sites.
func TestReferenceARNOrName(t *testing.T) {
	svc := Resource{
		Type:    "aws_ecs_service",
		Service: "ecs",
		References: []Reference{{
			Action:    "iam:PassRole",
			ARNFrom:   "iam_role",
			ARNFormat: "arn:${Partition}:iam::${Account}:role/${Name}",
			ARNOrName: &ARNOrName{Service: "iam", ResourceType: "role"},
		}},
	}
	ctx := ARNContext{Partition: "aws", Account: "123456789012", Region: "us-east-1"}
	want := "arn:aws:iam::123456789012:role/ecs-service"

	t.Run("bare role name is templated", func(t *testing.T) {
		got := svc.ResolveReferences(OpCreate, ctx, map[string]any{"iam_role": "ecs-service"}, nil, nil, nil)
		if len(got) != 1 || got[0].ARN != want || !got[0].Exact {
			t.Fatalf("got %+v, want one exact %q", got, want)
		}
	})

	// Used as written rather than decomposed and rebuilt: the role may live in
	// another account, and rebuilding would silently move it into the caller's.
	t.Run("role arn in another account is used verbatim", func(t *testing.T) {
		v := "arn:aws:iam::999999999999:role/ecs-service"
		got := svc.ResolveReferences(OpCreate, ctx, map[string]any{"iam_role": v}, nil, nil, nil)
		if len(got) != 1 || got[0].ARN != v || !got[0].Exact {
			t.Fatalf("got %+v, want one exact %q", got, v)
		}
	})

	// Before arn_or_name this passed the raw string through as though it were
	// an ARN, marked exact. That is the regression this guards.
	t.Run("value that is neither is inexact, never passed through", func(t *testing.T) {
		for _, v := range []string{"arn:aws:iam::123456789012:user/deploy", "some:thing"} {
			got := svc.ResolveReferences(OpCreate, ctx, map[string]any{"iam_role": v}, nil, nil, nil)
			if len(got) != 1 {
				t.Fatalf("%q: reference disappeared: %+v", v, got)
			}
			if got[0].Exact || got[0].ARN != "*" {
				t.Errorf("%q: got (%q, %v), want (%q, false)", v, got[0].ARN, got[0].Exact, "*")
			}
		}
	})
}

// TestReferenceWithoutARNOrNameIsUnchanged pins the backward-compatible
// behaviour every shipped reference relies on: no declaration means the value
// is used exactly as it was before this field existed.
func TestReferenceWithoutARNOrNameIsUnchanged(t *testing.T) {
	ctx := ARNContext{Partition: "aws", Account: "123456789012", Region: "us-east-1"}

	verbatim := Resource{References: []Reference{{Action: "iam:PassRole", ARNFrom: "role"}}}
	got := verbatim.ResolveReferences(OpCreate, ctx, map[string]any{"role": "not-an-arn"}, nil, nil, nil)
	if len(got) != 1 || got[0].ARN != "not-an-arn" || !got[0].Exact {
		t.Errorf("verbatim reference changed behaviour: %+v", got)
	}

	templated := Resource{References: []Reference{{
		Action:    "iam:PassRole",
		ARNFrom:   "role",
		ARNFormat: "arn:${Partition}:iam::${Account}:role/${Name}",
	}}}
	got = templated.ResolveReferences(OpCreate, ctx, map[string]any{"role": "arn:aws:iam::1:role/x"}, nil, nil, nil)
	want := "arn:aws:iam::123456789012:role/arn:aws:iam::1:role/x"
	if len(got) != 1 || got[0].ARN != want {
		t.Errorf("templated reference changed behaviour: %+v", got)
	}
}

// TestFixedTargetReference covers a `references` entry with no `arn_from`: the
// action is authorised against a resource the plan cannot name and never could.
// route53:GetChange is the motivating case — Route 53 hands back an ephemeral
// change id that the provider polls, so the only honest target is the wildcard a
// real policy grants.
func TestFixedTargetReference(t *testing.T) {
	res := Resource{
		Type:    "aws_route53_zone",
		Service: "route53",
		References: []Reference{{
			Action:    "route53:GetChange",
			ARNFormat: "arn:${Partition}:route53:::change/*",
		}},
		Operations: map[Operation][]Action{OpCreate: {{Action: "route53:CreateHostedZone"}}},
	}

	// The caller's partition is filled in; region and account are absent from the
	// template, which is correct for Route 53 and must not be "helpfully" added.
	ctx := ARNContext{Partition: "aws", Account: "123456789012", Region: "eu-west-2"}
	got := res.ResolveReferences(OpCreate, ctx, nil, nil, nil, nil)
	if len(got) != 1 {
		t.Fatalf("got %d referenced actions, want 1: %+v", len(got), got)
	}
	if got[0].ARN != "arn:aws:route53:::change/*" {
		t.Errorf("ARN = %q, want the fixed change wildcard", got[0].ARN)
	}
	// Exact, because every placeholder in the template was filled. A wildcard
	// inside the ARN is the policy's own shape, not an unresolved value, so
	// downgrading here would caveat a finding that needs no caveat.
	if !got[0].Exact {
		t.Error("want exact: the template resolved completely")
	}

	// No attributes are consulted at all, so an empty plan resolves it just the
	// same. That is the property that makes the form usable for an id that exists
	// only at apply time.
	if got2 := res.ResolveReferences(OpCreate, ctx, map[string]any{}, nil, nil, nil); len(got2) != 1 || got2[0].ARN != got[0].ARN {
		t.Errorf("resolution depended on plan attributes: %+v", got2)
	}
}

// TestFixedTargetReferenceRejectsName pins the load-time rejection. A ${Name}
// with no `arn_from` has nothing to fill it from, and an unfilled placeholder
// degrades the whole ARN to "*" — silently turning a scoped check into an
// unscoped one, which is the direction that hides a real denial.
func TestFixedTargetReferenceRejectsName(t *testing.T) {
	for name, ref := range map[string]Reference{
		"${Name} with nothing to fill it": {
			Action:    "route53:GetChange",
			ARNFormat: "arn:${Partition}:route53:::change/${Name}",
		},
		"neither arn_from nor arn_format": {
			Action: "route53:GetChange",
		},
		"arn_or_name with no attribute to classify": {
			Action:    "route53:GetChange",
			ARNFormat: "arn:${Partition}:route53:::change/*",
			ARNOrName: &ARNOrName{Service: "route53", ResourceType: "change"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			res := Resource{
				Type:       "aws_probe",
				Service:    "route53",
				Status:     StatusDraft,
				References: []Reference{ref},
				Operations: map[Operation][]Action{OpCreate: {{Action: "route53:CreateHostedZone"}}},
			}
			if err := res.validate(); err == nil {
				t.Error("validate accepted it")
			}
		})
	}
}
