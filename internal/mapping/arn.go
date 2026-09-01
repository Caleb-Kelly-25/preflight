package mapping

import (
	"regexp"
	"strings"
)

// varPattern matches a ${Var} placeholder in an ARN template.
var varPattern = regexp.MustCompile(`\$\{([A-Za-z0-9_]+)\}`)

// ARNContext supplies the account-level values that every ARN template needs.
type ARNContext struct {
	Partition string
	Account   string
	Region    string
}

// BuildARN fills the resource's ARN template from the planned attributes.
//
// It reports exact=false when any placeholder could not be resolved — because
// the attribute is absent, or because Terraform marked it unknown-until-apply.
// In that case the returned ARN is "*", and the caller MUST downgrade the
// finding's confidence: simulating against a wildcard cannot prove that a
// policy scoped to specific ARNs will allow the action.
//
// This is the central accuracy problem in the whole tool. Creates are exactly
// the case where the resource does not exist yet, so its identifying attributes
// are most likely to be unknown. See docs/ARCHITECTURE.md, open question 1.
func (r Resource) BuildARN(ctx ARNContext, after, afterUnknown map[string]any) (arn string, exact bool) {
	if r.ARNFormat == "" {
		return "*", false
	}

	resolved := true
	out := varPattern.ReplaceAllStringFunc(r.ARNFormat, func(match string) string {
		name := varPattern.FindStringSubmatch(match)[1]

		switch name {
		case "Partition":
			if ctx.Partition == "" {
				resolved = false
				return match
			}
			return ctx.Partition
		case "Account":
			if ctx.Account == "" {
				resolved = false
				return match
			}
			return ctx.Account
		case "Region":
			if ctx.Region == "" {
				resolved = false
				return match
			}
			return ctx.Region
		}

		attr, ok := r.ARNAttributes[name]
		if !ok {
			resolved = false
			return match
		}
		if isUnknown(afterUnknown, attr) {
			resolved = false
			return match
		}
		v, ok := after[attr]
		if !ok || v == nil {
			resolved = false
			return match
		}
		s, ok := v.(string)
		if !ok || s == "" {
			resolved = false
			return match
		}
		return s
	})

	if !resolved {
		return "*", false
	}
	return out, true
}

// isUnknown reports whether Terraform marked an attribute as unknown until
// apply. In plan JSON, after_unknown mirrors the attribute tree with `true` at
// any position whose value is not yet known.
func isUnknown(afterUnknown map[string]any, attr string) bool {
	if afterUnknown == nil {
		return false
	}
	v, ok := afterUnknown[attr]
	if !ok {
		return false
	}
	b, ok := v.(bool)
	return ok && b
}

// ARNVars lists the template variables this resource's ARN format needs,
// excluding the account-level ones supplied by ARNContext. Useful for
// validating that every variable has an arn_attributes entry.
func (r Resource) ARNVars() []string {
	var out []string
	for _, m := range varPattern.FindAllStringSubmatch(r.ARNFormat, -1) {
		name := m[1]
		switch name {
		case "Partition", "Account", "Region":
			continue
		}
		if !contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if strings.EqualFold(s, needle) {
			return true
		}
	}
	return false
}
