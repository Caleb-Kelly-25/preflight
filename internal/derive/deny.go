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

// canonicalAction lowercases the SERVICE PREFIX and leaves the action name alone.
//
// Services do not agree on the casing they hand back. SNS denies with
// "SNS:SetTopicAttributes" while IAM and S3 use a lowercase prefix, and IAM
// treats action names case-insensitively so all of them authorise identically.
// The mapping database does not: `TestShippedDatabase` requires every action to
// use its resource's declared service prefix, which is lowercase, and the
// evidence check compares action strings EXACTLY. An uncanonicalised name
// therefore lands in mappings/evidence/<type>.json as "SNS:SetTopicAttributes",
// which can never match the "sns:SetTopicAttributes" a correct entry lists — so
// the evidence would contradict the entry forever, for a reason that has nothing
// to do with permissions.
//
// Only the prefix is touched. Lowercasing the whole string would produce
// "sns:settopicattributes", which authorises fine but is not the canonical
// spelling anyone reviewing a mapping would recognise.
func canonicalAction(a string) string {
	service, action, found := strings.Cut(a, ":")
	if !found {
		return a
	}
	return strings.ToLower(service) + ":" + action
}

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
			a := canonicalAction(strings.TrimSpace(m[1]))
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
