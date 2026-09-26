package derive_test

import (
	"slices"
	"testing"

	"github.com/Caleb-Kelly-25/preflight/internal/derive"
)

// These are real denial strings captured while deriving mappings by hand, with
// account ids replaced. AWS's format is not uniform across services and is not
// documented as a contract, so the parser is pinned against what AWS actually
// said rather than against what the docs describe.
func TestParseDenial(t *testing.T) {
	tests := map[string]struct {
		text       string
		want       []string
		wantOpaque bool
	}{
		// IAM, 2026-09-02. The finding that AddRoleToInstanceProfile needs
		// PassRole against the role rather than the profile.
		"iam names the action": {
			text: `An error occurred (AccessDenied) when calling the AddRoleToInstanceProfile operation: ` +
				`User: arn:aws:sts::123456789012:assumed-role/pf-probe/pr is not authorized to perform: ` +
				`iam:PassRole on resource: arn:aws:iam::123456789012:role/pf-target because no ` +
				`identity-based policy allows the iam:PassRole action.`,
			want: []string{"iam:PassRole"},
		},
		// S3, 2026-09-01, from the resource-policy measurement. Note the
		// different tail: "with an explicit deny in a resource-based policy".
		"s3 explicit deny in a resource policy": {
			text: `An error occurred (AccessDenied) when calling the PutBucketVersioning operation: ` +
				`User: arn:aws:sts::123456789012:assumed-role/pf-rp-probe/rp-test is not authorized to ` +
				`perform: s3:PutBucketVersioning on resource: "arn:aws:s3:::pf-rp-denied" with an ` +
				`explicit deny in a resource-based policy`,
			want: []string{"s3:PutBucketVersioning"},
		},
		// EC2, 2026-09-02. This one was expected to be opaque and is not: the
		// action is named alongside the encoded blob. Worth pinning, because
		// the whole EC2 derivation strategy turns on it.
		"ec2 names the action despite the encoded message": {
			text: `Error: deleting EC2 VPC (vpc-066589fb): operation error EC2: DeleteVpc, ` +
				`https response error StatusCode: 403, api error UnauthorizedOperation: You are not ` +
				`authorized to perform this operation. User: arn:aws:sts::123456789012:assumed-role/` +
				`pf-vpc/nodel is not authorized to perform: ec2:DeleteVpc on resource: ` +
				`arn:aws:ec2:us-east-1:123456789012:vpc/vpc-066589fb because no identity-based policy ` +
				`allows the ec2:DeleteVpc action. Encoded authorization failure message: Inydy-i-eywSF`,
			want: []string{"ec2:DeleteVpc"},
		},
		// The genuinely opaque shape. Reporting this as opaque rather than
		// guessing is the whole point: an invented action name sends the loop
		// chasing a permission that does not exist.
		"encoded message with no action named": {
			text: `api error UnauthorizedOperation: You are not authorized to perform this operation. ` +
				`Encoded authorization failure message: 8Fj2kQ-pLm4nR7sT`,
			wantOpaque: true,
		},
		"several actions in one message": {
			text: `is not authorized to perform: s3:GetBucketAcl on resource X; ` +
				`is not authorized to perform: s3:GetBucketCORS on resource Y`,
			want: []string{"s3:GetBucketAcl", "s3:GetBucketCORS"},
		},
		"a plain failure is neither": {
			text: `Error: creating EC2 VPC: InvalidVpcRange: The CIDR '10.0.0.0/8' is invalid.`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, opaque := derive.ParseDenial(tc.text)
			if !slices.Equal(got, tc.want) {
				t.Errorf("actions = %v, want %v", got, tc.want)
			}
			if opaque != tc.wantOpaque {
				t.Errorf("opaque = %v, want %v", opaque, tc.wantOpaque)
			}
		})
	}
}

// A denial naming an action must never also report opaque: the caller branches
// on opacity to decide whether it can make progress at all.
func TestNamedDenialIsNeverOpaque(t *testing.T) {
	got, opaque := derive.ParseDenial("is not authorized to perform: iam:CreateRole on resource: x")
	if opaque {
		t.Error("a denial that named an action was also reported opaque")
	}
	if len(got) != 1 {
		t.Fatalf("actions = %v, want one", got)
	}
}

// TestDenialActionPrefixIsCanonicalised covers a real corruption found on
// 2026-09-26 while deriving aws_sns_topic. SNS denies with an UPPERCASE service
// prefix — "SNS:SetTopicAttributes" — and the parser took it verbatim, so the
// measured set was written into mappings/evidence/aws_sns_topic.json in a casing
// that can never match what a correct entry lists.
//
// IAM authorises either spelling, so nothing failed at AWS. The damage was
// downstream: the evidence check compares action strings exactly, and
// TestShippedDatabase requires the lowercase declared prefix, so the evidence and
// the entry would have disagreed permanently for a reason unrelated to
// permissions.
func TestDenialActionPrefixIsCanonicalised(t *testing.T) {
	for name, tc := range map[string]struct{ text, want string }{
		"SNS uppercases its prefix": {
			text: "AccessDenied: User: arn:aws:iam::1:user/x is not authorized to perform: SNS:SetTopicAttributes on resource: arn:aws:sns:us-east-1:1:t",
			want: "sns:SetTopicAttributes",
		},
		"a lowercase prefix is left alone": {
			text: "is not authorized to perform: iam:PassRole on resource: arn:aws:iam::1:role/r",
			want: "iam:PassRole",
		},
		"mixed case in the prefix only": {
			text: "is not authorized to perform: DynamoDB:CreateTable on resource: arn:aws:dynamodb:us-east-1:1:table/t",
			want: "dynamodb:CreateTable",
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, opaque := derive.ParseDenial(tc.text)
			if opaque {
				t.Fatalf("reported opaque; want the action %q", tc.want)
			}
			if len(got) != 1 || got[0] != tc.want {
				t.Errorf("got %v, want [%s]", got, tc.want)
			}
		})
	}
}

// TestDenialActionNameCaseIsPreserved pins the other half: only the prefix is
// lowercased. "sns:settopicattributes" authorises identically but is not the
// spelling a reviewer would recognise in a mapping, and the database is meant to
// be checkable by eye.
func TestDenialActionNameCaseIsPreserved(t *testing.T) {
	got, _ := derive.ParseDenial("is not authorized to perform: SNS:SetTopicAttributes on resource: x")
	if len(got) != 1 || got[0] != "sns:SetTopicAttributes" {
		t.Errorf("got %v, want [sns:SetTopicAttributes]", got)
	}
}
