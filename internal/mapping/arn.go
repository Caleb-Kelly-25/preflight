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

// ReferencedAction is one action required against another resource's ARN.
type ReferencedAction struct {
	Action string
	ARN    string
	// Exact is false when the referenced ARN could not be resolved, in which
	// case ARN is "*" and any denial is inconclusive — same rule as BuildARN.
	Exact bool
}

// ResolveReferences returns the cross-resource actions this operation needs,
// each paired with the ARN it must be evaluated against.
func (r Resource) ResolveReferences(op Operation, ctx ARNContext, attrs, unknown, before, after map[string]any) []ReferencedAction {
	var out []ReferencedAction
	for _, ref := range r.References {
		if !ref.AppliesTo(op) {
			continue
		}
		if !conditionHolds(ref.When, attrs, unknown, before, after) {
			continue
		}
		arn, exact := ref.buildARN(ctx, attrs, unknown)
		out = append(out, ReferencedAction{Action: ref.Action, ARN: arn, Exact: exact})
	}
	return out
}

// buildARN resolves the referenced resource's ARN. The attribute usually holds
// a complete ARN already (aws_lambda_function.role); ARNFormat covers the case
// where it holds a bare name instead (aws_iam_instance_profile.role).
func (ref Reference) buildARN(ctx ARNContext, attrs, unknown map[string]any) (string, bool) {
	if isUnknown(unknown, ref.ARNFrom) {
		return "*", false
	}
	v, ok := attrs[ref.ARNFrom].(string)
	if !ok || v == "" {
		return "*", false
	}
	if ref.ARNFormat == "" {
		return v, true
	}

	resolved := true
	out := varPattern.ReplaceAllStringFunc(ref.ARNFormat, func(match string) string {
		switch varPattern.FindStringSubmatch(match)[1] {
		case "Name":
			return v
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
		resolved = false
		return match
	})
	if !resolved {
		return "*", false
	}
	return out, true
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
	// approximate marks an ARN built from a name_prefix rather than a real
	// name. The string is usable, but it is not the resource's actual ARN.
	approximate := false
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

		if s, ok := stringAttr(after, afterUnknown, r.ARNAttributes[name]); ok {
			return s
		}

		// Fall back to a name_prefix attribute. Terraform generates the real
		// name at apply time, so it is genuinely unknowable here — but a
		// representative name sharing the prefix is still far more useful than
		// "*", because policies are commonly scoped with a trailing wildcard
		// ("role/myapp-*"). A concrete name matches that; "*" does not, as E1
		// measured. The result is never exact, so an allow can only reach
		// Likely and a denial stays inconclusive.
		if s, ok := stringAttr(after, afterUnknown, r.ARNPrefixAttributes[name]); ok {
			approximate = true
			return s + prefixPlaceholder
		}

		resolved = false
		return match
	})

	if !resolved {
		return "*", false
	}
	return out, !approximate
}

// prefixPlaceholder stands in for the suffix Terraform appends to a name_prefix.
// Twenty-six characters, matching the length Terraform actually generates, so
// the synthetic name cannot exceed a length limit the real one would respect.
const prefixPlaceholder = "00000000000000000000000000"

// stringAttr reads a non-empty string attribute that Terraform already knows.
func stringAttr(after, afterUnknown map[string]any, attr string) (string, bool) {
	if attr == "" || isUnknown(afterUnknown, attr) {
		return "", false
	}
	s, ok := after[attr].(string)
	if !ok || s == "" {
		return "", false
	}
	return s, true
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
