// Package simulate implements engine.Simulator against the real AWS IAM policy
// simulator.
//
// Everything that touches AWS is behind the narrow interfaces in this file.
// That seam is what lets every test in this package run with no credentials, no
// network and no build tag — only the opt-in contract tests talk to AWS.
package simulate

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// iamAPI is the slice of the IAM SDK this package uses.
//
// Deliberately not *iam.Client: taking the concrete type would make every test
// in the package require credentials, and this is a tool whose whole premise is
// that it is safe to hand AWS credentials to.
type iamAPI interface {
	SimulatePrincipalPolicy(context.Context, *iam.SimulatePrincipalPolicyInput, ...func(*iam.Options)) (*iam.SimulatePrincipalPolicyOutput, error)
	GetContextKeysForPrincipalPolicy(context.Context, *iam.GetContextKeysForPrincipalPolicyInput, ...func(*iam.Options)) (*iam.GetContextKeysForPrincipalPolicyOutput, error)
}

// stsAPI is the slice of the STS SDK this package uses.
type stsAPI interface {
	GetCallerIdentity(context.Context, *sts.GetCallerIdentityInput, ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error)
}
