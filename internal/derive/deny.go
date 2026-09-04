package derive

import (
	"regexp"
	"sort"
	"strings"
)

// AWS denial messages are not uniform across services, so these patterns come
// from strings actually captured during the manual derivations rather than from
// documentation.
//
//	IAM  : "is not authorized to perform: iam:PassRole on resource: arn:..."
//	S3   : "is not authorized to perform: s3:GetBucketTagging on resource: ..."
//	EC2  : "is not authorized to perform: ec2:DeleteVpc on resource: arn:...
//	        Encoded authorization failure message: <base64>"
//
// EC2 was expected to be opaque — UnauthorizedOperation carrying only an
// encoded message — and sometimes is. But the DeleteVpc denial measured on
// 2026-09-02 named the action plainly alongside the encoded blob, so the
// parser tries the named form first and only reports opacity when that fails.
var notAuthorized = regexp.MustCompile(`not authorized to perform:?\s+([a-zA-Z0-9-]+:[A-Za-z0-9]+)`)

// explicitDeny matches a denial caused by a policy that names the action, which
// reads differently but carries the same information.
var explicitDeny = regexp.MustCompile(`([a-zA-Z0-9-]+:[A-Za-z0-9]+)\s+on resource[^,]*with an explicit deny`)

// opaqueDenial marks a refusal that names no action at all.
var opaqueDenial = regexp.MustCompile(`(?i)UnauthorizedOperation|AccessDenied|not authorized`)

// ParseDenial extracts the actions a failure blames, and reports whether the
// text is a denial that named nothing.
//
// Returning opaque=true rather than guessing is deliberate: inventing an action
// name would send the loop chasing a permission that does not exist, and
// silently attributing a hang to the wrong action is how a derived set ends up
// missing the real one.
func ParseDenial(text string) (actions []string, opaque bool) {
	seen := map[string]bool{}
	add := func(matches [][]string) {
		for _, m := range matches {
			a := strings.TrimSpace(m[1])
			if a != "" && !seen[a] {
				seen[a] = true
				actions = append(actions, a)
			}
		}
	}
	add(notAuthorized.FindAllStringSubmatch(text, -1))
	add(explicitDeny.FindAllStringSubmatch(text, -1))

	if len(actions) > 0 {
		sort.Strings(actions)
		return actions, false
	}
	return nil, opaqueDenial.MatchString(text)
}
