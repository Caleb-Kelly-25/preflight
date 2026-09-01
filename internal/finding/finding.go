// Package finding holds the confidence model — the single most important design
// constraint in the spec.
//
// Every checked resource change lands in exactly one of three levels, and a
// check that could not be fully performed must never present as safe. A false
// "safe" is more damaging to trust than a visible "not checked".
package finding

import "sort"

// Level is the confidence attached to a finding.
type Level string

const (
	// LevelVerified means the required actions were simulated against the
	// identity policy and any in-scope SCPs, with nothing left unevaluated: a
	// resolvable resource ARN, no resource-based policy or RCP in play, no
	// missing condition-key values, and a verified mapping entry.
	LevelVerified Level = "verified"

	// LevelLikely means the identity-policy simulation passed, but something
	// that could still deny at apply time was not evaluated. See Reason.
	LevelLikely Level = "likely"

	// LevelUnchecked means no meaningful check happened at all — most often an
	// unmapped resource type. Never treat this as passing.
	LevelUnchecked Level = "unchecked"
)

// Rank orders levels from most to least trustworthy, for threshold comparisons.
func (l Level) Rank() int {
	switch l {
	case LevelVerified:
		return 0
	case LevelLikely:
		return 1
	default:
		return 2
	}
}

// Reason records why a finding could not reach LevelVerified. A finding may
// carry several. These are deliberately specific: "likely" with no explanation
// is the kind of vague hedge that trains users to ignore it.
type Reason string

const (
	// ReasonResourcePolicyNotEvaluated is set when the target resource type can
	// carry its own policy (S3 bucket policy, KMS key policy, and so on).
	//
	// This is permanent, not a gap we can close. SimulatePrincipalPolicy does
	// not fetch resource policies, and refuses to evaluate a supplied one for
	// IAM roles at all — which is what CI deploys always use.
	ReasonResourcePolicyNotEvaluated Reason = "resource_policy_not_evaluated"

	// ReasonRCPNotEvaluated is set when the resource's service is governed by
	// AWS Organizations resource control policies. The simulator evaluates
	// SCPs but explicitly does not support RCPs, so an RCP could still deny
	// the action at apply time.
	//
	// Note there is no ReasonSCPNotEvaluated. SCPs in scope, including their
	// condition keys and resource scoping, ARE evaluated by
	// SimulatePrincipalPolicy, server-side, with no extra caller permission.
	// See docs/DESIGN.md §0.1 — this was the premise the original spec got
	// wrong, and it must not creep back in.
	ReasonRCPNotEvaluated Reason = "rcp_not_evaluated"

	// ReasonMappingUnverified is set when a contributing mapping entry is
	// still status: draft — written from working knowledge and not checked
	// against AWS's Service Authorization Reference.
	//
	// A Verified finding built on an unverified action list is a false "safe"
	// one level down, so draft mappings cap the result at Likely.
	ReasonMappingUnverified Reason = "mapping_unverified"

	// ReasonOperationNotMapped is set when the resource type is mapped but
	// this particular operation is not. Distinct from ReasonNoMapping so the
	// report can tell a user which half of the coverage gap they hit.
	ReasonOperationNotMapped Reason = "operation_not_mapped"

	// ReasonSimulationFailed is set when the actions were never evaluated —
	// the call failed, was throttled past its retries, or no simulator was
	// configured at all.
	ReasonSimulationFailed Reason = "simulation_failed"

	// Note there is no ReasonPrincipalPathUnknown. An assumed-role session ARN
	// omits the role's IAM path, which looked like it would break simulation —
	// but role names are unique account-wide, so a pathless ARN resolves
	// correctly. Measured, experiment E8c. See internal/principal.

	// ReasonARNUnresolved is set when the resource ARN could not be built
	// because the plan marks the identifying attributes as unknown until apply.
	// The actions were simulated against "*" instead.
	//
	// Measured 2026-08-31 (experiments E1, E5): "*" does NOT match a policy
	// scoped to specific ARNs — it returns implicitDeny. So a wildcard result
	// is trustworthy in one direction only. An "allowed" means the policy
	// grants broadly enough to cover whatever the real ARN turns out to be,
	// and is informative. A denial means only that the policy is scoped, and
	// says nothing about whether the real ARN falls inside that scope — so the
	// engine downgrades a wildcard denial to Unchecked rather than reporting a
	// missing permission that may not be missing.
	ReasonARNUnresolved Reason = "arn_not_resolvable"

	// ReasonConditionKeysUnknown is set when a policy the principal carries
	// references a condition key whose value we could not source from the plan.
	//
	// Set REGARDLESS of what the simulator decided. An unsupplied key can
	// produce a denial (which we then suppress as inconclusive) or a confident
	// "allowed" based on a value AWS substituted for us. The second is the
	// dangerous one, so the decision cannot be part of the trigger.
	ReasonConditionKeysUnknown Reason = "condition_keys_not_supplied"

	// ReasonContextKeysUnknowable is set when we could not discover which
	// condition keys the principal's policies reference at all — so we cannot
	// know whether any result depended on a value we failed to supply. Every
	// result in the run is then suspect in both directions.
	ReasonContextKeysUnknowable Reason = "context_keys_unknowable"

	// ReasonNoMapping is set when the resource type is absent from the mapping
	// database. Always paired with LevelUnchecked.
	ReasonNoMapping Reason = "resource_type_not_mapped"
)

// Decision is the outcome of simulating one action.
type Decision string

const (
	DecisionAllowed      Decision = "allowed"
	DecisionImplicitDeny Decision = "implicitDeny"
	DecisionExplicitDeny Decision = "explicitDeny"
	// DecisionNotSimulated means the action was never sent to AWS.
	DecisionNotSimulated Decision = "not_simulated"
)

// Denied reports whether the decision blocks the action.
func (d Decision) Denied() bool {
	return d == DecisionImplicitDeny || d == DecisionExplicitDeny
}

// ActionResult is one IAM action evaluated against one resource ARN.
type ActionResult struct {
	Action string `json:"action"`
	// ResourceARN is what the action was simulated against. "*" means the
	// concrete ARN was not derivable from the plan.
	ResourceARN string   `json:"resource_arn"`
	Decision    Decision `json:"decision"`

	// MissingContextValues names condition keys the simulator needed and we did
	// not supply.
	//
	// Reliable on a DENIAL. NOT reliable on an ALLOW: measured 2026-08-31, a
	// policy gated on aws:RequestedRegion=us-east-1 with no value supplied
	// returns allowed with this field EMPTY, because the simulator
	// auto-populates that key with a fixed us-east-1. So this is only half the
	// condition-key signal; the other half is GetContextKeysForPrincipalPolicy.
	// See docs/DESIGN.md §7.3.
	MissingContextValues []string `json:"missing_context_values,omitempty"`

	// Inconclusive marks a decision that must not be acted on. Two measured
	// cases produce a denial that proves nothing:
	//
	//   - ResourceArns "*" against an ARN-scoped policy (E1, E5). The denial
	//     proves only that the policy is scoped, not that the real ARN falls
	//     outside that scope.
	//   - A denial naming a condition key we did not supply (C1). The denial
	//     proves only that we did not supply it.
	//
	// Reporting either as a missing permission is the false positive that
	// trains teams to bypass the tool. The raw Decision is kept for --explain,
	// but Denied and MissingActions ignore inconclusive results.
	Inconclusive       bool   `json:"inconclusive,omitempty"`
	InconclusiveReason Reason `json:"inconclusive_reason,omitempty"`
}

// Finding is the result for one operation on one planned resource change.
type Finding struct {
	ResourceAddress string         `json:"resource_address"`
	ResourceType    string         `json:"resource_type"`
	Operation       string         `json:"operation"`
	Level           Level          `json:"level"`
	Reasons         []Reason       `json:"reasons,omitempty"`
	Actions         []ActionResult `json:"actions"`

	// SimulatedARN is what we actually sent as ResourceArns. Distinct from the
	// ARN on each ActionResult when the finding was never simulated at all.
	SimulatedARN string `json:"simulated_arn,omitempty"`

	// UnsuppliedContextKeys names condition keys the principal's policies
	// reference that we could not source from the plan.
	//
	// Populated regardless of the decision, because the silent-allow trap fires
	// on the allow path: an unsupplied key can produce a confident "allowed"
	// with nothing in MissingContextValues to warn us. See docs/DESIGN.md §7.3.
	UnsuppliedContextKeys []string `json:"unsupplied_context_keys,omitempty"`

	// SuppliedContext records the condition-key values we sent. Retained for
	// --explain, which is the only mitigation available for the SCP diagnostic
	// blind spot in §7.4.
	SuppliedContext []ContextEntry `json:"supplied_context,omitempty"`
}

// Denied reports whether any required action was denied by a result we can act
// on. Inconclusive denials are excluded — see ActionResult.Inconclusive. This is
// what keeps a wildcard-ARN denial out of "missing permissions" and out of the
// exit code, per docs/DESIGN.md §6.3.
func (f Finding) Denied() bool {
	for _, a := range f.Actions {
		if a.Inconclusive {
			continue
		}
		if a.Decision.Denied() {
			return true
		}
	}
	return false
}

// MissingActions returns the actions that were denied conclusively, for the
// report. Inconclusive denials are excluded for the same reason as in Denied.
func (f Finding) MissingActions() []string {
	var out []string
	for _, a := range f.Actions {
		if a.Inconclusive {
			continue
		}
		if a.Decision.Denied() {
			out = append(out, a.Action)
		}
	}
	return out
}

// SuppressedDenials returns actions AWS denied but which we cannot act on,
// with the reason each was suppressed. The report surfaces these so a user is
// never surprised by AWS saying no and preflight saying nothing.
func (f Finding) SuppressedDenials() []ActionResult {
	var out []ActionResult
	for _, a := range f.Actions {
		if a.Inconclusive && a.Decision.Denied() {
			out = append(out, a)
		}
	}
	return out
}

// AddReason records a reason, keeping the set unique and stably ordered.
func (f *Finding) AddReason(r Reason) {
	for _, existing := range f.Reasons {
		if existing == r {
			return
		}
	}
	f.Reasons = append(f.Reasons, r)
	sort.Slice(f.Reasons, func(i, j int) bool { return f.Reasons[i] < f.Reasons[j] })
}

// Report is the full result of one preflight run.
type Report struct {
	PrincipalARN string    `json:"principal_arn"`
	AccountID    string    `json:"account_id"`
	Findings     []Finding `json:"findings"`
	// Warnings are run-level caveats that are not tied to one resource.
	Warnings []string `json:"warnings,omitempty"`
}

// Counts summarises a report by confidence level.
type Counts struct {
	Verified  int `json:"verified"`
	Likely    int `json:"likely"`
	Unchecked int `json:"unchecked"`
	Denied    int `json:"denied"`
}

// Counts tallies the report.
func (r *Report) Counts() Counts {
	var c Counts
	for _, f := range r.Findings {
		switch f.Level {
		case LevelVerified:
			c.Verified++
		case LevelLikely:
			c.Likely++
		default:
			c.Unchecked++
		}
		if f.Denied() {
			c.Denied++
		}
	}
	return c
}

// FailThreshold controls which findings make a CI run fail.
type FailThreshold string

const (
	// FailOnDenied fails only on a confirmed missing permission. The default:
	// it is the one state we are certain about.
	FailOnDenied FailThreshold = "denied"
	// FailOnLikely additionally fails when anything could not be fully
	// verified.
	FailOnLikely FailThreshold = "likely"
	// FailOnUnchecked additionally fails when any resource type was unmapped.
	FailOnUnchecked FailThreshold = "unchecked"
)

// ShouldFail reports whether the report trips the given threshold.
func (r *Report) ShouldFail(t FailThreshold) bool {
	c := r.Counts()
	if c.Denied > 0 {
		return true
	}
	switch t {
	case FailOnLikely:
		return c.Likely > 0 || c.Unchecked > 0
	case FailOnUnchecked:
		return c.Unchecked > 0
	default:
		return false
	}
}
