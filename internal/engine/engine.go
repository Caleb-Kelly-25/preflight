// Package engine turns a parsed Terraform plan into a confidence-classified
// report.
//
// It performs no AWS calls itself. Everything that touches the network arrives
// through the Simulator interface, in a single Resolve call, which keeps the
// classification logic — the part that must never be wrong — testable entirely
// offline.
//
// The flow is three phases, two of them pure:
//
//	collect  (pure)  plan + mapping database  -> []unit
//	resolve          Request                  -> Response
//	classify (pure)  unit + ItemOutcome       -> finding.Finding
//
// Collecting every question before asking any of them is what lets the
// simulator batch. See simulator.go.
package engine

import (
	"context"
	"fmt"

	"github.com/Caleb-Kelly-25/preflight/internal/finding"
	"github.com/Caleb-Kelly-25/preflight/internal/mapping"
	"github.com/Caleb-Kelly-25/preflight/internal/plan"
	"github.com/Caleb-Kelly-25/preflight/internal/principal"
)

// Options configures an analysis run.
type Options struct {
	// Database is the resource-type-to-action mapping. Required.
	Database *mapping.Database

	// Identity is the resolved deploying principal.
	Identity principal.Identity

	// Simulator performs the IAM evaluation. When nil, nothing is simulated and
	// every finding degrades to Unchecked — an offline dry run.
	Simulator Simulator

	// Region fills ${Region} in ARN templates and supplies aws:RequestedRegion
	// when a resource does not carry its own.
	Region string

	// PolicyContextKeys are the condition keys the principal's policies
	// reference, from iam:GetContextKeysForPrincipalPolicy. This is the only
	// reliable up-front signal that a key is in play, because
	// MissingContextValues is trustworthy on a denial but not on an allow.
	PolicyContextKeys []string

	// ContextKeysUnknown reports that the above could not be determined, so we
	// cannot know whether any result depended on a value we failed to supply.
	ContextKeysUnknown bool

	// ExtraContext are operator-supplied --context values. They win over
	// anything sourced from the plan; it is the escape hatch for keys a plan
	// cannot supply, such as aws:SourceIp.
	ExtraContext []finding.ContextEntry
}

// Analyze classifies every actionable change in the plan.
func Analyze(ctx context.Context, p *plan.Plan, opts Options) (*finding.Report, error) {
	if opts.Database == nil {
		return nil, fmt.Errorf("engine: Options.Database is required")
	}

	rep := &finding.Report{
		PrincipalARN: opts.Identity.PolicySourceARN,
		AccountID:    opts.Identity.AccountID,
	}

	if p.UnsupportedFormat() {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf(
			"plan format version %s is newer than this build understands (expected %s.x); results may be incomplete",
			p.FormatVersion, plan.SupportedFormatMajor))
	}
	if opts.Simulator == nil {
		rep.Warnings = append(rep.Warnings,
			"no simulator configured: nothing was evaluated against AWS, every result is Unchecked")
	}
	if opts.ContextKeysUnknown {
		rep.Warnings = append(rep.Warnings,
			"could not determine which condition keys the principal's policies reference: "+
				"no result can be fully verified, because an allow may depend on a value AWS substituted")
	}

	arnCtx := mapping.ARNContext{
		Partition: partitionOr(opts.Identity.Partition),
		Account:   opts.Identity.AccountID,
		Region:    opts.Region,
	}

	units := collect(p, opts, arnCtx)
	req := buildRequest(units, opts.Identity.PolicySourceARN)

	outcomes, simulated := opts.resolve(ctx, req, rep)

	rep.Findings = make([]finding.Finding, 0, len(units))
	for _, u := range units {
		// One outcome per group, index-aligned with u.groups so classify can
		// tell which ARN each result belongs to.
		outs := make([]ItemOutcome, len(u.groups))
		for i, g := range u.groups {
			if g.reqIndex >= 0 && g.reqIndex < len(outcomes) {
				outs[i] = outcomes[g.reqIndex]
			}
		}
		rep.Findings = append(rep.Findings, classify(u, outs, simulated))
	}
	return rep, nil
}

// resolve performs the one network call, and normalises every failure into
// "nothing was evaluated". It never returns a partial result the caller could
// mistake for a complete one.
func (opts Options) resolve(ctx context.Context, req Request, rep *finding.Report) ([]ItemOutcome, bool) {
	if opts.Simulator == nil || len(req.Items) == 0 {
		return nil, false
	}

	resp, err := opts.Simulator.Resolve(ctx, req)
	if err != nil {
		rep.Warnings = append(rep.Warnings,
			fmt.Sprintf("simulation failed, every result is Unchecked: %v", err))
		return nil, false
	}

	// The interface contract is index alignment. A simulator that breaks it is
	// a bug we must not paper over by indexing blindly, and must not resolve by
	// guessing which outcome belongs to which item.
	if len(resp.Outcomes) != len(req.Items) {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf(
			"simulator returned %d outcomes for %d questions; results cannot be matched, so every result is Unchecked",
			len(resp.Outcomes), len(req.Items)))
		return nil, false
	}

	rep.Warnings = append(rep.Warnings, resp.Warnings...)
	return resp.Outcomes, true
}

func partitionOr(p string) string {
	if p == "" {
		return "aws"
	}
	return p
}
