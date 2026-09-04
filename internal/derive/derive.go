package derive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"
)

// Grantor owns the scratch role's permissions. Every method is reversible, and
// Revoke is idempotent because it runs on paths that may already have run.
type Grantor interface {
	// Grant replaces the role's entire permission set with exactly these
	// actions and waits for the change to be observable.
	Grant(ctx context.Context, actions []string) error
	// Revoke removes the role and everything attached to it.
	Revoke(ctx context.Context) error
}

// Applier runs one Terraform lifecycle against one fixture.
type Applier interface {
	// Apply runs terraform apply under the scratch role's credentials,
	// returning OutcomeStalled rather than blocking past budget.
	Apply(ctx context.Context, budget time.Duration) Outcome
	// Destroy tears the fixture down. It MUST run with operator credentials,
	// never the scratch role's: teardown cannot be allowed to fail for want of
	// the very permission being searched for.
	Destroy(ctx context.Context) error
}

// Deriver runs the loop.
type Deriver struct {
	Grantor Grantor
	Applier Applier

	// MaxAttempts caps the discovery phase, so a fixture that denies in a cycle
	// cannot spin forever.
	MaxAttempts int
	// AttemptBudget is how long one apply may run before it counts as stalled.
	AttemptBudget time.Duration

	Log io.Writer
}

// Result is what one derivation established.
type Result struct {
	Sufficient []string // proven: an apply succeeded with exactly this set
	Seed       []string // what the mapping claimed going in
	Missing    []string // Sufficient - Seed. The dangerous direction.
	Surplus    []string // Seed - Sufficient. Over-reporting; fails safe.

	Evidence map[string]Evidence

	Sufficiency Confidence
	Minimal     Confidence

	Attempts []Attempt
	Warnings []string

	// Dirty records that a teardown failed, so AWS may still hold resources
	// from this run and nothing measured afterwards can be trusted.
	Dirty bool
}

// Derive grows the seed set until an apply succeeds, then removes actions one
// at a time to find which are load-bearing.
func (d *Deriver) Derive(ctx context.Context, seed []string) (Result, error) {
	if d.Grantor == nil || d.Applier == nil {
		return Result{}, fmt.Errorf("derive: Grantor and Applier are required")
	}
	budget := d.AttemptBudget
	if budget <= 0 {
		budget = 5 * time.Minute
	}
	maxAttempts := d.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 12
	}

	res := Result{
		Seed:     dedupeSorted(seed),
		Evidence: map[string]Evidence{},
	}
	current := dedupeSorted(seed)

	// Phase 1 — grow. Cheap, and allowed to give up: it is an optimisation, not
	// the proof.
	grown := false
	for i := 0; i < maxAttempts; i++ {
		out, err := d.attempt(ctx, &res, current, budget)
		if err != nil {
			res.Sufficiency = ConfidenceInconclusive
			return res, err
		}
		switch out.Kind {
		case OutcomeSuccess:
			grown = true
		case OutcomeDenied:
			before := len(current)
			current = dedupeSorted(append(current, out.DeniedActions...))
			if len(current) == before {
				// The denial named nothing new, so another round would ask the
				// identical question. Stop rather than spin.
				res.Sufficiency = ConfidenceInconclusive
				res.Warnings = append(res.Warnings, fmt.Sprintf(
					"denial named %v, which was already granted; cannot make progress", out.DeniedActions))
				return res, nil
			}
			continue
		case OutcomeStalled:
			// A hang names no action, so discovery cannot attribute it. Report
			// honestly rather than guessing which permission was missing.
			res.Sufficiency = ConfidenceInconclusive
			res.Warnings = append(res.Warnings, fmt.Sprintf(
				"apply stalled at %s with no attributable denial; a missing permission that hangs "+
					"rather than failing needs the debug-log path", out.StalledAt))
			return res, nil
		case OutcomeOpaque:
			res.Sufficiency = ConfidenceInconclusive
			res.Warnings = append(res.Warnings,
				"denial did not name an action; this service needs sts:DecodeAuthorizationMessage")
			return res, nil
		default:
			res.Sufficiency = ConfidenceInconclusive
			res.Warnings = append(res.Warnings, fmt.Sprintf("apply failed for a non-IAM reason: %s", out.Detail))
			return res, nil
		}
		break
	}
	if !grown {
		res.Sufficiency = ConfidenceInconclusive
		res.Warnings = append(res.Warnings, fmt.Sprintf("no successful apply within %d attempts", maxAttempts))
		return res, nil
	}

	res.Sufficiency = ConfidenceProven
	res.Sufficient = current

	// Phase 2 — minimise. Every action is removed in turn and the apply re-run.
	res.Minimal = ConfidenceProven
	for _, action := range append([]string(nil), current...) {
		trial := without(current, action)
		if len(trial) == len(current) {
			continue
		}
		out, err := d.attempt(ctx, &res, trial, budget)
		if err != nil {
			// The apply itself ran in a clean environment — the previous
			// teardown succeeded, or we would already have stopped. So this
			// measurement is sound and worth keeping; it is the NEXT one that
			// would be unreliable. Record it, then stop.
			if out.Kind == OutcomeDenied || out.Kind == OutcomeStalled {
				res.Evidence[action] = Evidence{
					Action: action, Kind: out.Kind, Detail: out.Detail, Attempt: len(res.Attempts) - 1,
				}
			}
			res.Minimal = ConfidenceInconclusive
			return res, err
		}

		if out.Kind == OutcomeSuccess {
			// Asymmetric on purpose. Reading a transient failure as
			// "load-bearing" over-reports, which is visible and cheap. Reading
			// one as "droppable" writes a verified mapping that is MISSING an
			// action — the one unforgivable failure. So dropping needs two
			// successes; keeping needs one failure.
			confirm, err := d.attempt(ctx, &res, trial, budget)
			if err != nil {
				res.Minimal = ConfidenceInconclusive
				return res, err
			}
			if confirm.Kind == OutcomeSuccess {
				current = trial
				continue
			}
			res.Warnings = append(res.Warnings, fmt.Sprintf(
				"%s: first removal succeeded but the confirming run did not (%s); keeping it",
				action, confirm.Kind))
			res.Minimal = ConfidencePartial
			out = confirm
		}

		res.Evidence[action] = Evidence{
			Action:  action,
			Kind:    out.Kind,
			Detail:  out.Detail,
			Attempt: len(res.Attempts),
		}
		// Only a clean denial proves the action was needed. A stall or an
		// unrelated failure is weaker evidence and is recorded as such.
		if out.Kind != OutcomeDenied {
			res.Minimal = ConfidencePartial
		}
	}

	res.Sufficient = dedupeSorted(current)
	res.Missing = difference(res.Sufficient, res.Seed)
	res.Surplus = difference(res.Seed, res.Sufficient)
	return res, nil
}

// errDirty means teardown failed, so the environment no longer matches what the
// next attempt would assume.
var errDirty = errors.New("teardown failed; the environment is dirty and further results would be unreliable")

// attempt grants a set, applies, and always tears down.
//
// A failed teardown aborts the run. Continuing would measure the next attempt
// against leftover infrastructure, and an apply that "succeeds" only because the
// resource already exists is precisely how a derivation produces an action set
// that is missing something.
func (d *Deriver) attempt(ctx context.Context, res *Result, actions []string, budget time.Duration) (Outcome, error) {
	idx := len(res.Attempts)
	if err := d.Grantor.Grant(ctx, actions); err != nil {
		out := Outcome{Kind: OutcomeFailed, Detail: fmt.Sprintf("granting: %v", err)}
		res.Attempts = append(res.Attempts, Attempt{Index: idx, Granted: actions, Outcome: out})
		return out, nil
	}

	out := d.Applier.Apply(ctx, budget)

	// Teardown runs on every path, including the ones that failed.
	destroyErr := d.Applier.Destroy(ctx)

	res.Attempts = append(res.Attempts, Attempt{Index: idx, Granted: actions, Outcome: out})
	d.logf("attempt %d: %d actions -> %s", idx, len(actions), out.Kind)

	if destroyErr != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"attempt %d: destroy failed (%v); stopping rather than measuring against leftover resources",
			idx, destroyErr))
		res.Dirty = true
		return out, errDirty
	}
	return out, nil
}

func (d *Deriver) logf(format string, args ...any) {
	if d.Log == nil {
		return
	}
	fmt.Fprintf(d.Log, format+"\n", args...)
}

func without(actions []string, drop string) []string {
	out := make([]string, 0, len(actions))
	for _, a := range actions {
		if a != drop {
			out = append(out, a)
		}
	}
	return out
}

func difference(a, b []string) []string {
	in := make(map[string]bool, len(b))
	for _, s := range b {
		in[s] = true
	}
	var out []string
	for _, s := range a {
		if !in[s] {
			out = append(out, s)
		}
	}
	return out
}

func dedupeSorted(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
