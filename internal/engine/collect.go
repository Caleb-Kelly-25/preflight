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

	res     mapping.Resource
	mapped  bool
	actions []string

	arn      string
	arnExact bool

	supplied   []finding.ContextEntry
	unsupplied []string

	// reasons are the caveats knowable without simulating.
	reasons []finding.Reason

	// reqIndex is this unit's position in Request.Items, or -1 when the unit
	// was decided without simulating (unmapped type, unmapped operation, no
	// simulator). Those consume no request slot, so resources we were never
	// going to check do not dilute batching.
	reqIndex int
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
		reqIndex:     -1,
	}

	res, mapped := opts.Database.Lookup(rc.Type)
	if !mapped {
		u.addReason(finding.ReasonNoMapping)
		return u
	}
	u.res, u.mapped = res, true

	actions := res.Operations[mapping.Operation(op)]
	if len(actions) == 0 {
		// The type is mapped but this operation is not. Still a coverage gap,
		// not a pass.
		u.addReason(finding.ReasonOperationNotMapped)
		return u
	}
	// Every operation also needs the resource's read set: Terraform reads a
	// resource back after writing it, and a missing read permission fails the
	// apply just as surely as a missing write one. Copied rather than appended
	// in place so the mapping's own slice is never mutated.
	combined := make([]string, 0, len(actions)+len(res.ReadActions))
	combined = append(combined, actions...)
	combined = append(combined, res.ReadActions...)
	u.actions = dedupeSorted(combined)

	// Deletes act on the prior state, creates and updates on the planned state.
	attrs, unknown := rc.Change.After, rc.Change.AfterUnknown
	if op == plan.ActionDelete {
		attrs, unknown = rc.Change.Before, nil
	}

	u.arn, u.arnExact = res.BuildARN(arnCtx, attrs, unknown)
	if !u.arnExact {
		u.addReason(finding.ReasonARNUnresolved)
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
		if !u.mapped || len(u.actions) == 0 {
			continue
		}
		u.reqIndex = len(req.Items)
		req.Items = append(req.Items, RequestItem{
			Actions:     u.actions,
			ResourceARN: u.arn,
			Context:     u.supplied,
		})
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
