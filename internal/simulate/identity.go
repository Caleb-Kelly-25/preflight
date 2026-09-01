package simulate

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/Caleb-Kelly-25/preflight/internal/principal"
)

// ErrNoCredentials is returned when the SDK found no usable credentials.
var ErrNoCredentials = errors.New("no AWS credentials found")

// ResolveIdentity is the only place preflight learns who it is.
//
// It calls sts:GetCallerIdentity and hands the ARN to principal.Resolve, which
// performs the assumed-role-session to role translation that
// SimulatePrincipalPolicy requires — inside a GitHub Actions job the caller ARN
// is a session ARN, which the simulator rejects verbatim.
func ResolveIdentity(ctx context.Context, api stsAPI) (principal.Identity, error) {
	out, err := api.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		if isCredentialError(err) {
			return principal.Identity{}, ErrNoCredentials
		}
		if f := fatal(err, "", "sts:GetCallerIdentity"); f != nil {
			return principal.Identity{}, f
		}
		return principal.Identity{}, fmt.Errorf("resolving caller identity: %w", err)
	}
	if out.Arn == nil || *out.Arn == "" {
		return principal.Identity{}, fmt.Errorf("sts:GetCallerIdentity returned no ARN")
	}
	return principal.Resolve(*out.Arn)
}

// isCredentialError reports whether the SDK failed because it could not find or
// load credentials, as opposed to AWS rejecting a request it did make. The two
// need very different messages: one is "configure AWS", the other is "add a
// permission".
func isCredentialError(err error) bool {
	if err == nil {
		return false
	}
	// The SDK surfaces this as a chain of wrapped provider errors rather than a
	// single sentinel, so matching on the message is the available option.
	msg := strings.ToLower(err.Error())
	for _, marker := range []string{
		"failed to refresh cached credentials",
		"no ec2 imds role found",
		"failed to retrieve credentials",
		"nocredentialproviders",
		"an aws region is required",
		"ssoproviderinvalidtoken",
		"cannot retrieve credentials",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}
