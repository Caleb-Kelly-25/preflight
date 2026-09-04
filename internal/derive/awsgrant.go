//go:build awsderive

package derive

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
)

// iamAPI and stsAPI name only the calls this package makes, mirroring
// internal/simulate/awsapi.go. Narrow interfaces are what let the real client
// be swapped for a fake without a build tag on the tests.
type iamAPI interface {
	CreateRole(context.Context, *iam.CreateRoleInput, ...func(*iam.Options)) (*iam.CreateRoleOutput, error)
	PutRolePolicy(context.Context, *iam.PutRolePolicyInput, ...func(*iam.Options)) (*iam.PutRolePolicyOutput, error)
	DeleteRolePolicy(context.Context, *iam.DeleteRolePolicyInput, ...func(*iam.Options)) (*iam.DeleteRolePolicyOutput, error)
	ListRolePolicies(context.Context, *iam.ListRolePoliciesInput, ...func(*iam.Options)) (*iam.ListRolePoliciesOutput, error)
	DeleteRole(context.Context, *iam.DeleteRoleInput, ...func(*iam.Options)) (*iam.DeleteRoleOutput, error)
}

type stsAPI interface {
	AssumeRole(context.Context, *sts.AssumeRoleInput, ...func(*sts.Options)) (*sts.AssumeRoleOutput, error)
}

// Credentials are what the scratch role hands to Terraform.
type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

// AWSGrantor manages a scratch role whose permission set is rewritten between
// attempts.
type AWSGrantor struct {
	iam  iamAPI
	sts  stsAPI
	role string // role name
	arn  string
	// Propagation is how long to wait after a policy write before assuming.
	// IAM is eventually consistent, and a stale grant reads exactly like a
	// missing permission — which would add phantom actions to the derived set.
	Propagation time.Duration

	// Latest holds the credentials from the most recent Grant.
	Latest Credentials
}

// NewAWSGrantor wires the real clients. The narrow-interface constructor below
// is what tests use.
func NewAWSGrantor(cfg aws.Config, roleName, roleARN string) *AWSGrantor {
	return newAWSGrantor(iam.NewFromConfig(cfg), sts.NewFromConfig(cfg), roleName, roleARN)
}

func newAWSGrantor(i iamAPI, s stsAPI, roleName, roleARN string) *AWSGrantor {
	return &AWSGrantor{iam: i, sts: s, role: roleName, arn: roleARN, Propagation: 10 * time.Second}
}

// Grant replaces the role's inline policy with exactly these actions, waits for
// propagation, and assumes the role.
func (g *AWSGrantor) Grant(ctx context.Context, actions []string) error {
	doc, err := json.Marshal(map[string]any{
		"Version": "2012-10-17",
		"Statement": []map[string]any{
			{"Effect": "Allow", "Action": actions, "Resource": "*"},
		},
	})
	if err != nil {
		return fmt.Errorf("building policy document: %w", err)
	}
	if len(actions) == 0 {
		// An empty Action list is not a valid policy. Delete the policy instead,
		// which is the same thing semantically: the role can do nothing.
		if _, err := g.iam.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{
			RoleName: aws.String(g.role), PolicyName: aws.String(derivePolicyName),
		}); err != nil && !isNoSuchEntity(err) {
			return fmt.Errorf("clearing policy: %w", err)
		}
	} else if _, err := g.iam.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
		RoleName:       aws.String(g.role),
		PolicyName:     aws.String(derivePolicyName),
		PolicyDocument: aws.String(string(doc)),
	}); err != nil {
		return fmt.Errorf("writing policy: %w", err)
	}

	select {
	case <-time.After(g.Propagation):
	case <-ctx.Done():
		return ctx.Err()
	}

	out, err := g.sts.AssumeRole(ctx, &sts.AssumeRoleInput{
		RoleArn:         aws.String(g.arn),
		RoleSessionName: aws.String(fmt.Sprintf("derive-%d", time.Now().Unix())),
		DurationSeconds: aws.Int32(3600),
	})
	if err != nil {
		return fmt.Errorf("assuming scratch role: %w", err)
	}
	if out.Credentials == nil {
		// Never leave stale credentials in place on a failed assume: reusing
		// the previous attempt's session is exactly how a derivation run
		// silently reports every action as unnecessary.
		g.Latest = Credentials{}
		return fmt.Errorf("assume returned no credentials")
	}
	g.Latest = Credentials{
		AccessKeyID:     aws.ToString(out.Credentials.AccessKeyId),
		SecretAccessKey: aws.ToString(out.Credentials.SecretAccessKey),
		SessionToken:    aws.ToString(out.Credentials.SessionToken),
	}
	return nil
}

// Revoke deletes the inline policy and then the role. Idempotent: it runs on
// paths that may already have run, including teardown after a crash.
func (g *AWSGrantor) Revoke(ctx context.Context) error {
	// IAM refuses to delete a role that still has inline policies.
	pols, err := g.iam.ListRolePolicies(ctx, &iam.ListRolePoliciesInput{RoleName: aws.String(g.role)})
	if err != nil && !isNoSuchEntity(err) {
		return fmt.Errorf("listing role policies: %w", err)
	}
	if pols != nil {
		for _, name := range pols.PolicyNames {
			if _, err := g.iam.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{
				RoleName: aws.String(g.role), PolicyName: aws.String(name),
			}); err != nil && !isNoSuchEntity(err) {
				return fmt.Errorf("deleting role policy %s: %w", name, err)
			}
		}
	}
	if _, err := g.iam.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(g.role)}); err != nil && !isNoSuchEntity(err) {
		return fmt.Errorf("deleting role: %w", err)
	}
	return nil
}

const derivePolicyName = "preflight-derive"

func isNoSuchEntity(err error) bool {
	var ae smithy.APIError
	if !errorsAs(err, &ae) {
		return false
	}
	return ae.ErrorCode() == "NoSuchEntity"
}
