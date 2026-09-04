package mapping

import "testing"

func TestBuildARN(t *testing.T) {
	s3 := Resource{
		Type:          "aws_s3_bucket",
		Service:       "s3",
		ARNFormat:     "arn:${Partition}:s3:::${BucketName}",
		ARNAttributes: map[string]string{"BucketName": "bucket"},
	}
	ctx := ARNContext{Partition: "aws", Account: "123456789012", Region: "us-east-1"}

	t.Run("known attribute produces an exact arn", func(t *testing.T) {
		got, exact := s3.BuildARN(ctx, map[string]any{"bucket": "my-logs"}, nil)
		if !exact {
			t.Error("exact = false, want true")
		}
		if want := "arn:aws:s3:::my-logs"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	// The central accuracy problem: on create, the identifying attribute is
	// frequently unknown until apply, so we cannot scope the simulation.
	t.Run("unknown-until-apply attribute falls back to wildcard", func(t *testing.T) {
		got, exact := s3.BuildARN(ctx,
			map[string]any{"bucket": nil},
			map[string]any{"bucket": true})
		if exact {
			t.Error("exact = true, want false: the bucket name is not known until apply")
		}
		if got != "*" {
			t.Errorf("got %q, want %q", got, "*")
		}
	})

	t.Run("missing attribute falls back to wildcard", func(t *testing.T) {
		got, exact := s3.BuildARN(ctx, map[string]any{}, nil)
		if exact {
			t.Error("exact = true, want false")
		}
		if got != "*" {
			t.Errorf("got %q, want %q", got, "*")
		}
	})

	t.Run("account and region placeholders are filled from context", func(t *testing.T) {
		r := Resource{
			ARNFormat:     "arn:${Partition}:ec2:${Region}:${Account}:instance/${InstanceId}",
			ARNAttributes: map[string]string{"InstanceId": "id"},
		}
		got, exact := r.BuildARN(ctx, map[string]any{"id": "i-abc123"}, nil)
		if !exact {
			t.Error("exact = false, want true")
		}
		if want := "arn:aws:ec2:us-east-1:123456789012:instance/i-abc123"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("empty arn format is never exact", func(t *testing.T) {
		got, exact := Resource{}.BuildARN(ctx, nil, nil)
		if exact || got != "*" {
			t.Errorf("got (%q, %v), want (%q, false)", got, exact, "*")
		}
	})
}

// TestBuildARNFromPrefix covers name_prefix resources, where the real name is
// generated at apply time.
//
// The point is not to guess the name — that is impossible — but to beat "*".
// M1 measured that ResourceArns is a literal rather than a pattern, so passing
// "myapp-*" would match nothing. A concrete representative name does match a
// policy scoped as "role/myapp-*", which is how such policies are actually
// written, turning an Unchecked into a Likely.
func TestBuildARNFromPrefix(t *testing.T) {
	role := Resource{
		Type:                "aws_iam_role",
		Service:             "iam",
		ARNFormat:           "arn:${Partition}:iam::${Account}:role/${RoleName}",
		ARNAttributes:       map[string]string{"RoleName": "name"},
		ARNPrefixAttributes: map[string]string{"RoleName": "name_prefix"},
	}
	ctx := ARNContext{Partition: "aws", Account: "123456789012", Region: "us-east-1"}

	t.Run("a real name still wins over the prefix", func(t *testing.T) {
		got, exact := role.BuildARN(ctx,
			map[string]any{"name": "deploy", "name_prefix": "deploy-"}, nil)
		if !exact {
			t.Error("exact = false, want true: the name is known")
		}
		if want := "arn:aws:iam::123456789012:role/deploy"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("prefix produces a representative arn, never exact", func(t *testing.T) {
		got, exact := role.BuildARN(ctx,
			map[string]any{"name": nil, "name_prefix": "myapp-"},
			map[string]any{"name": true})
		if exact {
			t.Error("exact = true, but the real name is generated at apply time")
		}
		if got == "*" {
			t.Fatal("fell back to wildcard despite a usable name_prefix")
		}
		want := "arn:aws:iam::123456789012:role/myapp-" + prefixPlaceholder
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("no name and no prefix is still a wildcard", func(t *testing.T) {
		got, exact := role.BuildARN(ctx, map[string]any{}, nil)
		if exact || got != "*" {
			t.Errorf("got (%q, %v), want (%q, false)", got, exact, "*")
		}
	})
}

func TestARNVars(t *testing.T) {
	r := Resource{ARNFormat: "arn:${Partition}:iam::${Account}:role/${RoleName}"}
	got := r.ARNVars()
	if len(got) != 1 || got[0] != "RoleName" {
		t.Errorf("ARNVars() = %v, want [RoleName]", got)
	}
}
