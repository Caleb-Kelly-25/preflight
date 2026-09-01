package simulate

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
)

// ReferencedContextKeys asks AWS which condition keys the principal's own
// policies actually reference.
//
// This is the only reliable up-front signal that a key is in play.
// MissingContextValues is trustworthy on a denial but not on an allow: measured
// 2026-08-31, a policy gated on aws:RequestedRegion with no value supplied
// returns allowed with that field EMPTY, because the simulator substitutes its
// own value. Without this probe, that silent false negative is undetectable.
//
// Note it reflects identity policies only — condition keys referenced solely by
// an SCP are invisible to it, which is the residual blind spot in
// docs/DESIGN.md §7.4.
func ReferencedContextKeys(ctx context.Context, api iamAPI, policySourceARN string) ([]string, error) {
	out, err := api.GetContextKeysForPrincipalPolicy(ctx, &iam.GetContextKeysForPrincipalPolicyInput{
		PolicySourceArn: aws.String(policySourceARN),
	})
	if err != nil {
		if f := fatal(err, policySourceARN, "iam:GetContextKeysForPrincipalPolicy"); f != nil {
			return nil, f
		}
		return nil, fmt.Errorf("discovering referenced condition keys: %w", err)
	}
	return out.ContextKeyNames, nil
}
