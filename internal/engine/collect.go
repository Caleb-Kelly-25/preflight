package engine

import (
	"sort"

	"github.com/Caleb-Kelly-25/preflight/internal/finding"
	"github.com/Caleb-Kelly-25/preflight/internal/mapping"
	"github.com/Caleb-Kelly-25/preflight/internal/plan"
)

// unit is one (resource change × operation), fully prepared for classification.
//
// Everything the classifier needs is gathered here, before any network access,
// which is what keeps classification pure and testable without credentials.
type unit struct {
	address      string
	resourceType string
	operation    string

	res    mapping.Resource
	mapped bool

	// groups are the questions this unit needs answered. groups[0] is always
	// the resource's own ARN; any further groups come from `references` and
	// target a DIFFERENT resource — iam:PassRole is checked against the role
	// being handed over, not against the thing doing the handing.
	//
	// One unit therefore spans several request items, which is why the engine
	// cannot assume a unit maps to exactly one.
	groups []actionGroup

	// arn and arnExact describe the resource's own ARN, kept separately because
	// they are what the finding reports and what the ARN caveat is about.
	arn      string
	arnExact bool

	supplied   []finding.ContextEntry
	unsupplied []string

	// reasons are the caveats knowable without simulating.
	reasons []finding.Reason
}

// actionGroup is one set of actions to evaluate against one ARN.
type actionGroup struct {
	actions []string
	arn     string
	exact   bool
	// reqIndex is this group's position in Request.Items, or -1 when it was
	// decided without simulating. Groups that consume no slot do not dilute
	// batching.
	reqIndex int
}

// allActions is every action across every group, for the decided-without-
// simulating paths.
func (u unit) allActions() []string {
	var out []string
	for _, g := range u.groups {
		out = append(out, g.actions...)
	}
	return out
}

func (u *unit) addReason(r finding.Reason) {
	for _, existing := range u.reasons {
		if existing == r {
			return
		}
	}
	u.reasons = append(u.reasons, r)
}

// collect walks the plan and prepares one unit per actionable change ×
// operation. Pure: no I/O, no AWS, no clock.
func collect(p *plan.Plan, opts Options, arnCtx mapping.ARNContext) []unit {
	var units []unit
	for _, rc := range p.Actionable() {
		for _, op := range rc.Change.Operations() {
			units = append(units, opts.prepare(rc, op, arnCtx))
		}
	}
	return units
}

// prepare builds one unit, resolving everything decidable offline.
func (opts Options) prepare(rc plan.ResourceChange, op plan.Action, arnCtx mapping.ARNContext) unit {
	u := unit{
		address:      rc.Address,
		resourceType: rc.Type,
		operation:    string(op),
	}

	res, mapped := opts.Database.Lookup(rc.Type)
	if !mapped {
		u.addReason(finding.ReasonNoMapping)
		return u
	}
	u.res, u.mapped = res, true

	if len(res.Operations[mapping.Operation(op)]) == 0 {
		// The type is mapped but this operation is not. Still a coverage gap,
		// not a pass.
		u.addReason(finding.ReasonOperationNotMapped)
		return u
	}

	// Deletes act on the prior state, creates and updates on the planned state.
	attrs, unknown := rc.Change.After, rc.Change.AfterUnknown
	if op == plan.ActionDelete {
		attrs, unknown = rc.Change.Before, nil
	}

	// Conditional actions are filtered against the actual attributes, so an
	// untagged resource is not asked for tagging permissions it will never use.
	actions := res.RequiredActions(mapping.Operation(op), attrs, unknown, rc.Change.Before, rc.Change.After)

	// Every operation also needs the resource's read set: Terraform reads a
	// resource back after writing it, and a missing read permission fails the
	// apply just as surely as a missing write one. Copied rather than appended
	// in place so the mapping's own slice is never mutated.
	combined := make([]string, 0, len(actions)+len(res.ReadActions))
	combined = append(combined, actions...)
	combined = append(combined, res.ReadActions...)

	u.arn, u.arnExact = res.BuildARN(arnCtx, attrs, unknown)
	if !u.arnExact {
		u.addReason(finding.ReasonARNUnresolved)
	}
	u.groups = []actionGroup{{
		actions:  dedupeSorted(combined),
		arn:      u.arn,
		exact:    u.arnExact,
		reqIndex: -1,
	}}

	// Cross-resource requirements are evaluated against the resource they point
	// at, so each distinct referenced ARN becomes its own question. Actions
	// sharing an ARN are grouped, so a resource passing the same role twice
	// costs one item rather than two.
	byARN := map[string][]string{}
	exactByARN := map[string]bool{}
	var arnOrder []string
	for _, ref := range res.ResolveReferences(mapping.Operation(op), arnCtx, attrs, unknown, rc.Change.Before, rc.Change.After) {
		if _, seen := byARN[ref.ARN]; !seen {
			arnOrder = append(arnOrder, ref.ARN)
			exactByARN[ref.ARN] = ref.Exact
		}
		byARN[ref.ARN] = append(byARN[ref.ARN], ref.Action)
	}
	for _, arn := range arnOrder {
		if !exactByARN[arn] {
			// Same rule as the resource's own ARN: a wildcard cannot prove an
			// ARN-scoped policy allows the real target.
			u.addReason(finding.ReasonARNUnresolved)
		}
		u.groups = append(u.groups, actionGroup{
			actions:  dedupeSorted(byARN[arn]),
			arn:      arn,
			exact:    exactByARN[arn],
			reqIndex: -1,
		})
	}
	if res.ResourcePolicyApplies(mapping.Operation(op)) {
		u.addReason(finding.ReasonResourcePolicyNotEvaluated)
	}
	if res.RCPGoverned() {
		u.addReason(finding.ReasonRCPNotEvaluated)
	}
	if !res.VerifiedFor(mapping.Operation(op)) {
		u.addReason(finding.ReasonMappingUnverified)
	}

	// Condition-key sourcing. See context.go for why the region is special.
	u.supplied, u.unsupplied = opts.buildContext(res, attrs, unknown)
	if opts.ContextKeysUnknown {
		u.addReason(finding.ReasonContextKeysUnknowable)
	}
	if len(u.unsupplied) > 0 {
		u.addReason(finding.ReasonConditionKeysUnknown)
	}

	return u
}

// buildRequest assembles the questions for every unit that needs simulating,
// stamping each unit with its index into the result. Pure.
func buildRequest(units []unit, policySourceARN string) Request {
	req := Request{PolicySourceARN: policySourceARN}
	for i := range units {
		u := &units[i]
		if !u.mapped {
			continue
		}
		for g := range u.groups {
			grp := &u.groups[g]
			if len(grp.actions) == 0 {
				continue
			}
			grp.reqIndex = len(req.Items)
			req.Items = append(req.Items, RequestItem{
				Actions:     grp.actions,
				ResourceARN: grp.arn,
				Context:     u.supplied,
			})
		}
	}
	return req
}

// dedupeSorted returns the actions deduped and sorted, so that two resources
// needing the same permissions produce byte-identical action sets and therefore
// hash into the same batch.
func dedupeSorted(actions []string) []string {
	if len(actions) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(actions))
	out := make([]string, 0, len(actions))
	for _, a := range actions {
		if seen[a] {
			continue
		}
		seen[a] = true
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}
