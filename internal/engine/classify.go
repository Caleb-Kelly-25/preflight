package engine

import "github.com/Caleb-Kelly-25/preflight/internal/finding"

// classify turns one prepared unit plus its simulation outcome into a finding.
//
// Pure, and deliberately so: this is the logic that must never be wrong, and it
// is exercised entirely without credentials. See docs/DESIGN.md §3.
func classify(u unit, out ItemOutcome, simulated bool) finding.Finding {
	f := finding.Finding{
		ResourceAddress:       u.address,
		ResourceType:          u.resourceType,
		Operation:             u.operation,
		SimulatedARN:          u.arn,
		UnsuppliedContextKeys: u.unsupplied,
		SuppliedContext:       u.supplied,
	}
	for _, r := range u.reasons {
		f.AddReason(r)
	}

	// Decided without simulating: unmapped type or unmapped operation. Both
	// already carry their reason from prepare.
	if !u.mapped || len(u.actions) == 0 {
		f.Level = finding.LevelUnchecked
		return f
	}

	// No simulator configured, or this item's batch never completed. Either way
	// nothing was evaluated, and nothing unevaluated may present as safe.
	if !simulated || u.reqIndex < 0 || out.Err != nil {
		f.Level = finding.LevelUnchecked
		f.AddReason(finding.ReasonSimulationFailed)
		f.Actions = notSimulated(u.actions, u.arn)
		return f
	}

	f.Actions = markInconclusive(out.Results, u)

	// A result naming a missing context value tells us a key was in play that
	// we did not supply, even when prepare could not predict it.
	for _, a := range f.Actions {
		if len(a.MissingContextValues) > 0 {
			f.AddReason(finding.ReasonConditionKeysUnknown)
			f.UnsuppliedContextKeys = mergeKeys(f.UnsuppliedContextKeys, a.MissingContextValues)
		}
	}

	f.Level = level(f)
	return f
}

// markInconclusive flags the decisions we measured to be untrustworthy. Both
// cases are denials that prove something narrower than they appear to.
func markInconclusive(results []finding.ActionResult, u unit) []finding.ActionResult {
	out := make([]finding.ActionResult, len(results))
	copy(out, results)

	for i := range out {
		if !out[i].Decision.Denied() {
			continue
		}
		switch {
		case len(out[i].MissingContextValues) > 0:
			// C1: the denial proves only that we failed to supply a key the
			// policy's condition needed.
			out[i].Inconclusive = true
			out[i].InconclusiveReason = finding.ReasonConditionKeysUnknown

		case !u.arnExact:
			// E1, E5: ResourceArns "*" does not match an ARN-scoped policy, so
			// the denial proves only that the policy is scoped — not that the
			// real ARN falls outside that scope. Reporting it as a missing
			// permission is the false positive that trains teams to bypass the
			// tool.
			out[i].Inconclusive = true
			out[i].InconclusiveReason = finding.ReasonARNUnresolved
		}
	}
	return out
}

// level applies the decision table. First match wins.
func level(f finding.Finding) finding.Level {
	// Nothing usable came back at all. A finding whose every result is
	// inconclusive or unsimulated has not been checked, whatever else is true
	// of it.
	if !anyConclusive(f.Actions) {
		return finding.LevelUnchecked
	}
	if len(f.Reasons) > 0 {
		return finding.LevelLikely
	}
	return finding.LevelVerified
}

func anyConclusive(results []finding.ActionResult) bool {
	for _, a := range results {
		if a.Inconclusive || a.Decision == finding.DecisionNotSimulated {
			continue
		}
		return true
	}
	return false
}

func notSimulated(actions []string, arn string) []finding.ActionResult {
	out := make([]finding.ActionResult, 0, len(actions))
	for _, a := range actions {
		out = append(out, finding.ActionResult{
			Action:      a,
			ResourceARN: arn,
			Decision:    finding.DecisionNotSimulated,
		})
	}
	return out
}

func mergeKeys(existing, add []string) []string {
	seen := make(map[string]bool, len(existing))
	for _, k := range existing {
		seen[k] = true
	}
	for _, k := range add {
		if !seen[k] {
			seen[k] = true
			existing = append(existing, k)
		}
	}
	return existing
}
