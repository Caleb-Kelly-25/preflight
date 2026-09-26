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
	// A reference with no `arn_from` names a FIXED target: the action is
	// authorised against a resource whose identity is not in the plan and never
	// could be. route53:GetChange is the case — Route 53 hands back an ephemeral
	// change id that the provider polls, and no plan attribute holds it, so the
	// only honest target is the change/* wildcard a real policy grants.
	//
	// The alternative was to fold such an action into read_actions, which scopes
	// it to the RESOURCE's own ARN. That is worse than imprecise, it is wrong:
	// asking AWS whether the caller may GetChange on a hostedzone ARN gets an
	// implicit deny, so a policy correctly granting it on change/* would be
	// reported as a missing permission on every plan.
	if ref.ARNFrom == "" {
		return templateARN(ref.ARNFormat, ctx, "")
	}
	if isUnknown(unknown, ref.ARNFrom) {
		return "*", false
	}
	v, ok := attrs[ref.ARNFrom].(string)
	if !ok || v == "" {
		return "*", false
	}
	if ref.ARNOrName != nil {
		switch kind, _ := ref.ARNOrName.classify(v); kind {
		case valueARN:
			// The attribute already holds the ARN we want, so there is nothing
			// to build. Deliberately used verbatim rather than decomposed and
			// re-templated: the caller's partition, account and region are not
			// necessarily the target's, and rebuilding would quietly relocate a
			// cross-account role into the account running terraform.
			return v, true
		case valueAmbiguous:
			// Neither spelling. Templating it would nest an unrecognised value
			// inside a synthetic ARN and report it with confidence.
			return "*", false
		}
		// valueName falls through to the template below, which is what the
		// declaration exists to make safe.
	}
	if ref.ARNFormat == "" {
		return v, true
	}

	return templateARN(ref.ARNFormat, ctx, v)
}

// templateARN fills ${Partition}, ${Account}, ${Region} from the caller and
// ${Name} from the referenced attribute's value.
//
// Any placeholder it cannot fill collapses the whole ARN to "*" and exact=false,
// rather than emitting a half-substituted string. A partially templated ARN
// would match no policy at all while looking like a real target, which reads as
// a permission gap that is not there.
//
// `name` is empty for a fixed-target reference, in which case a ${Name} in the
// template is unfillable and the ARN degrades — correctly, since such a template
// is a mistake rather than a wildcard.
func templateARN(format string, ctx ARNContext, name string) (string, bool) {
	resolved := true
	out := varPattern.ReplaceAllStringFunc(format, func(match string) string {
		switch varPattern.FindStringSubmatch(match)[1] {
		case "Name":
			if name == "" {
				resolved = false
				return match
			}
			return name
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

	// Polymorphic attributes are resolved BEFORE the substitution walk, not
	// during it, for two reasons. One is ordering: an attribute that turns out
	// to hold an ARN also tells us which partition, account and region the
	// target really lives in, and those placeholders appear earlier in the
	// template than the name does. The other is that a value we cannot identify
	// has to abort the whole ARN rather than one placeholder.
	names, ctx, ok := r.resolveARNOrName(ctx, after, afterUnknown)
	if !ok {
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

		// A value already normalised by arn_or_name wins: it is the same
		// attribute, read through the check that makes it safe to substitute.
		if s, ok := names[name]; ok {
			return s
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

// resolveARNOrName reads every attribute declared with arn_or_name and reduces
// it to the bare name the template needs, returning those names keyed by ARN
// variable together with the context the template should be built in.
//
// ok=false means a declared attribute held something that is neither a name nor
// a recognisable ARN. That aborts the whole ARN rather than one placeholder,
// because the alternative — substituting it anyway — is the exact failure this
// field was added to remove.
//
// When the value IS an ARN, its partition, account and region replace the
// caller's for the rest of the template. That is not a refinement, it is the
// correctness of the feature: an ECS service lives in the region and account of
// its cluster, not of whoever is running terraform, so a cluster ARN pointing at
// eu-west-1 must build a service ARN in eu-west-1. Using the caller's region
// there would produce a well-formed ARN naming a resource that does not exist —
// confidently wrong, which is worse than "*".
func (r Resource) resolveARNOrName(ctx ARNContext, after, afterUnknown map[string]any) (map[string]string, ARNContext, bool) {
	if len(r.ARNOrName) == 0 {
		return nil, ctx, true
	}

	names := make(map[string]string, len(r.ARNOrName))
	var adopted *parsedARN
	for v, decl := range r.ARNOrName {
		s, ok := stringAttr(after, afterUnknown, r.ARNAttributes[v])
		if !ok {
			// Absent, or unknown until apply. Not a failure here: the
			// substitution walk degrades it exactly as it degrades any other
			// unfillable variable, including falling back to a name_prefix.
			continue
		}
		kind, p := decl.classify(s)
		switch kind {
		case valueName:
			names[v] = s
		case valueARN:
			names[v] = p.ResourceID
			if adopted != nil && (adopted.Partition != p.Partition ||
				adopted.Region != p.Region || adopted.Account != p.Account) {
				// Two declared attributes disagree about where the target
				// lives. One of them must be wrong and nothing here can tell
				// which, so neither is used.
				return nil, ctx, false
			}
			q := p
			adopted = &q
		default:
			return nil, ctx, false
		}
	}

	if adopted != nil {
		// Empty segments are not adopted: an IAM ARN carries no region, and
		// overwriting the caller's with "" would break any template that uses
		// ${Region} for something else.
		if adopted.Partition != "" {
			ctx.Partition = adopted.Partition
		}
		if adopted.Region != "" {
			ctx.Region = adopted.Region
		}
		if adopted.Account != "" {
			ctx.Account = adopted.Account
		}
	}
	return names, ctx, true
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
