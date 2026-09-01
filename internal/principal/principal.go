// Package principal resolves the effective deploying identity into an ARN that
// iam:SimulatePrincipalPolicy will accept as its PolicySourceArn.
//
// This matters more than it looks. `aws sts get-caller-identity` inside a
// GitHub Actions job returns an *assumed-role session* ARN:
//
//	arn:aws:sts::123456789012:assumed-role/deploy-role/GitHubActions
//
// SimulatePrincipalPolicy will not accept that. It needs the underlying role:
//
//	arn:aws:iam::123456789012:role/deploy-role
//
// The Terraform AWS provider exposes the same translation as the
// `aws_iam_session_context` data source; this package is the equivalent logic.
package principal

import (
	"fmt"
	"strings"
)

// Kind classifies the caller identity, which determines whether and how it can
// be simulated.
type Kind string

const (
	KindIAMUser       Kind = "iam_user"
	KindIAMRole       Kind = "iam_role"
	KindAssumedRole   Kind = "assumed_role"
	KindRoot          Kind = "root"
	KindFederatedUser Kind = "federated_user"
	KindUnknown       Kind = "unknown"
)

// Identity is a caller identity resolved into simulatable form.
type Identity struct {
	// CallerARN is the ARN exactly as returned by sts:GetCallerIdentity.
	CallerARN string
	AccountID string
	Partition string
	Kind      Kind

	// PolicySourceARN is what to pass to SimulatePrincipalPolicy. Empty when
	// the identity cannot be simulated; see Simulatable.
	PolicySourceARN string

	// RoleName and SessionName are set for assumed-role identities.
	RoleName    string
	SessionName string
}

// Why there is no path handling here.
//
// An assumed-role session ARN omits the role's IAM path, so string parsing
// alone reconstructs a pathless ARN for a role that may live at, say,
// /platform/deploy-role. That looks like it should break simulation, and the
// original design budgeted an iam:GetRole call to repair it.
//
// Measured 2026-08-31 against a real account (experiment E8c): it does not
// break. SimulatePrincipalPolicy resolves a pathless ARN to the right role.
// IAM requires role names to be unique account-wide, so the name alone is an
// unambiguous key and the path is redundant for lookup.
//
// A genuinely nonexistent principal fails loudly with NoSuchEntity rather than
// returning a misleading denial (experiment E8d), so there is no silent-failure
// mode to defend against either.
//
// Net: no iam:GetRole call, no confidence downgrade, one less required
// permission. See docs/DESIGN.md §5.1.

// Simulatable reports whether this identity can be checked with
// iam:SimulatePrincipalPolicy.
func (i Identity) Simulatable() bool {
	return i.PolicySourceARN != "" && i.Kind != KindRoot
}

// Reason explains, for a non-simulatable identity, why it cannot be checked.
func (i Identity) Reason() string {
	switch i.Kind {
	case KindRoot:
		return "the account root user is being used; root bypasses IAM policy evaluation, so simulation would be meaningless. Deploy with an IAM role instead."
	case KindFederatedUser:
		return "federated users have no persistent IAM principal to simulate against."
	case KindUnknown:
		return "unrecognised caller ARN shape; cannot determine what to simulate."
	default:
		return ""
	}
}

// ARN is a parsed Amazon Resource Name.
type ARN struct {
	Partition string
	Service   string
	Region    string
	AccountID string
	Resource  string
}

// ParseARN splits an ARN into its six colon-delimited fields. The resource
// field may itself contain colons and slashes, so it is not split further.
func ParseARN(s string) (ARN, error) {
	parts := strings.SplitN(s, ":", 6)
	if len(parts) != 6 || parts[0] != "arn" {
		return ARN{}, fmt.Errorf("not a valid ARN: %q", s)
	}
	return ARN{
		Partition: parts[1],
		Service:   parts[2],
		Region:    parts[3],
		AccountID: parts[4],
		Resource:  parts[5],
	}, nil
}

// Resolve translates a caller ARN from sts:GetCallerIdentity into an Identity.
//
// It performs no AWS calls. For assumed roles it returns a pathless role ARN,
// which SimulatePrincipalPolicy accepts even when the role has a path — see the
// note above on why no iam:GetRole call is needed.
func Resolve(callerARN string) (Identity, error) {
	a, err := ParseARN(callerARN)
	if err != nil {
		return Identity{}, err
	}

	id := Identity{
		CallerARN: callerARN,
		AccountID: a.AccountID,
		Partition: a.Partition,
		Kind:      KindUnknown,
	}

	resType, rest, hasSlash := strings.Cut(a.Resource, "/")

	switch {
	case a.Service == "iam" && a.Resource == "root":
		id.Kind = KindRoot
		id.PolicySourceARN = callerARN

	case a.Service == "iam" && resType == "user" && hasSlash:
		// User ARNs already carry their full path, so nothing is lost here.
		id.Kind = KindIAMUser
		id.PolicySourceARN = callerARN

	case a.Service == "iam" && resType == "role" && hasSlash:
		// An already-translated role ARN. sts:GetCallerIdentity never returns
		// this shape, but users pass it to --principal constantly — it is what
		// they see in the console and what SimulatePrincipalPolicy wants anyway.
		id.Kind = KindIAMRole
		// rest may carry an IAM path; the role name is its final segment.
		if i := strings.LastIndex(rest, "/"); i >= 0 {
			id.RoleName = rest[i+1:]
		} else {
			id.RoleName = rest
		}
		if id.RoleName == "" {
			return id, fmt.Errorf("malformed role ARN: %q", callerARN)
		}
		id.PolicySourceARN = callerARN

	case a.Service == "sts" && resType == "assumed-role" && hasSlash:
		roleName, sessionName, ok := strings.Cut(rest, "/")
		if !ok || roleName == "" {
			return id, fmt.Errorf("malformed assumed-role ARN: %q", callerARN)
		}
		id.Kind = KindAssumedRole
		id.RoleName = roleName
		id.SessionName = sessionName
		id.PolicySourceARN = fmt.Sprintf("arn:%s:iam::%s:role/%s", a.Partition, a.AccountID, roleName)

	case a.Service == "sts" && resType == "federated-user":
		id.Kind = KindFederatedUser

	default:
		return id, fmt.Errorf("unrecognised caller identity ARN: %q", callerARN)
	}

	return id, nil
}
