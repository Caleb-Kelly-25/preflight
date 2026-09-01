package principal

import "testing"

func TestResolve(t *testing.T) {
	tests := []struct {
		name       string
		callerARN  string
		wantKind   Kind
		wantSource string
		wantErr    bool
	}{
		{
			// The case that matters most: this is what GitHub Actions OIDC
			// produces, and SimulatePrincipalPolicy rejects it verbatim.
			name:       "assumed role session becomes role arn",
			callerARN:  "arn:aws:sts::123456789012:assumed-role/deploy-role/GitHubActions",
			wantKind:   KindAssumedRole,
			wantSource: "arn:aws:iam::123456789012:role/deploy-role",
		},
		{
			name:       "session name containing an @",
			callerARN:  "arn:aws:sts::123456789012:assumed-role/deploy-role/alice@example.com",
			wantKind:   KindAssumedRole,
			wantSource: "arn:aws:iam::123456789012:role/deploy-role",
		},
		{
			name:       "iam user passes through unchanged",
			callerARN:  "arn:aws:iam::123456789012:user/alice",
			wantKind:   KindIAMUser,
			wantSource: "arn:aws:iam::123456789012:user/alice",
		},
		{
			name:       "iam user with a path keeps its path",
			callerARN:  "arn:aws:iam::123456789012:user/platform/alice",
			wantKind:   KindIAMUser,
			wantSource: "arn:aws:iam::123456789012:user/platform/alice",
		},
		{
			name:       "govcloud partition is preserved",
			callerARN:  "arn:aws-us-gov:sts::123456789012:assumed-role/deploy-role/session",
			wantKind:   KindAssumedRole,
			wantSource: "arn:aws-us-gov:iam::123456789012:role/deploy-role",
		},
		{
			name:       "root is recognised",
			callerARN:  "arn:aws:iam::123456789012:root",
			wantKind:   KindRoot,
			wantSource: "arn:aws:iam::123456789012:root",
		},
		{
			name:      "federated user is not simulatable",
			callerARN: "arn:aws:sts::123456789012:federated-user/bob",
			wantKind:  KindFederatedUser,
		},
		{
			name:      "garbage is rejected",
			callerARN: "not-an-arn",
			wantErr:   true,
		},
		{
			name:      "malformed assumed-role without session is rejected",
			callerARN: "arn:aws:sts::123456789012:assumed-role/",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Resolve(tt.callerARN)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Resolve(%q) = %+v, want error", tt.callerARN, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve(%q) returned unexpected error: %v", tt.callerARN, err)
			}
			if got.Kind != tt.wantKind {
				t.Errorf("Kind = %q, want %q", got.Kind, tt.wantKind)
			}
			if got.PolicySourceARN != tt.wantSource {
				t.Errorf("PolicySourceARN = %q, want %q", got.PolicySourceARN, tt.wantSource)
			}
			if got.AccountID != "123456789012" {
				t.Errorf("AccountID = %q, want 123456789012", got.AccountID)
			}
		})
	}
}

func TestSimulatable(t *testing.T) {
	// Root must never be simulated: it bypasses IAM evaluation entirely, so a
	// simulation would report a uniform "allowed" that means nothing.
	root, err := Resolve("arn:aws:iam::123456789012:root")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if root.Simulatable() {
		t.Error("root reported as simulatable; it bypasses IAM policy evaluation")
	}
	if root.Reason() == "" {
		t.Error("root has no explanatory Reason()")
	}

	role, err := Resolve("arn:aws:sts::123456789012:assumed-role/r/s")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !role.Simulatable() {
		t.Error("assumed role reported as not simulatable")
	}
}
