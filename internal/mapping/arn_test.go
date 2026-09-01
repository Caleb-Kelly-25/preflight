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

func TestARNVars(t *testing.T) {
	r := Resource{ARNFormat: "arn:${Partition}:iam::${Account}:role/${RoleName}"}
	got := r.ARNVars()
	if len(got) != 1 || got[0] != "RoleName" {
		t.Errorf("ARNVars() = %v, want [RoleName]", got)
	}
}
