package simulate

import (
	"sort"
	"strings"

	"github.com/Caleb-Kelly-25/preflight/internal/engine"
	"github.com/Caleb-Kelly-25/preflight/internal/finding"
)

// key identifies one question: does this principal have this action on this
// resource, under this context?
//
// The context fingerprint is part of the identity, not an afterthought. The same
// action against the same ARN can legitimately decide differently under
// different condition-key values, so caching on (action, arn) alone would serve
// a wrong answer.
type key struct {
	action string
	arn    string
	ctxFP  string
}

// batchKey is what makes two questions shareable in one API call.
//
// docs/DESIGN.md §9.2 originally grouped on the action set alone. That is a
// correctness bug once condition-key values are sourced per resource: IAM
// applies ContextEntries per CALL, not per resource, so two resources with
// different tag-derived values sharing a call would each be evaluated under the
// other's context — a silently wrong answer rather than an error.
type batchKey struct {
	actionSet string
	contextFP string
}

// batch is one or more API calls' worth of work: an action set evaluated
// against a set of ARNs under one context.
type batch struct {
	actions []string
	arns    []string
	entries []finding.ContextEntry
}

// groupBatches turns the outstanding questions into the fewest calls that
// answer them all without mixing contexts.
//
// maxProduct caps len(actions) x len(arns) per call. The response is the
// cartesian product of the two, so this is what keeps one call to one page and
// makes a partial-pagination failure cheap.
func groupBatches(items []engine.RequestItem, wanted map[key]bool, maxProduct int) []batch {
	if maxProduct < 1 {
		maxProduct = 1
	}

	type group struct {
		actions []string
		entries []finding.ContextEntry
		arns    map[string]bool
	}
	groups := map[batchKey]*group{}
	var order []batchKey

	for _, item := range items {
		fp := finding.Fingerprint(item.Context)

		// Only ask about pairs still outstanding — the rest were cache hits.
		var need bool
		for _, a := range item.Actions {
			if wanted[key{action: a, arn: item.ResourceARN, ctxFP: fp}] {
				need = true
				break
			}
		}
		if !need {
			continue
		}

		bk := batchKey{actionSet: strings.Join(item.Actions, "\x00"), contextFP: fp}
		g, ok := groups[bk]
		if !ok {
			g = &group{actions: item.Actions, entries: item.Context, arns: map[string]bool{}}
			groups[bk] = g
			order = append(order, bk)
		}
		g.arns[item.ResourceARN] = true
	}

	var out []batch
	for _, bk := range order {
		g := groups[bk]

		// The wildcard cannot share a call with concrete ARNs. Measured
		// 2026-09-01 against a real account:
		//
		//   InvalidInput: Invalid resource input list: you cannot include both
		//   * and individual resources in the resource list for a simulation.
		//
		// This is not in the API reference. Without the split, any plan mixing
		// a known-name resource with an unknown-name one of the same type fails
		// the whole batch — which is most real plans.
		var concrete, wildcard []string
		for a := range g.arns {
			if a == "*" {
				wildcard = append(wildcard, a)
			} else {
				concrete = append(concrete, a)
			}
		}
		// Sorted so batch composition is deterministic run to run, which keeps
		// failures reproducible.
		sort.Strings(concrete)

		perCall := maxProduct / max(1, len(g.actions))
		if perCall < 1 {
			perCall = 1
		}
		for _, arns := range [][]string{concrete, wildcard} {
			for start := 0; start < len(arns); start += perCall {
				end := min(start+perCall, len(arns))
				out = append(out, batch{
					actions: g.actions,
					arns:    arns[start:end],
					entries: g.entries,
				})
			}
		}
	}
	return out
}

// wantedKeys enumerates every question the request asks.
func wantedKeys(items []engine.RequestItem) map[key]bool {
	out := make(map[key]bool)
	for _, item := range items {
		fp := finding.Fingerprint(item.Context)
		for _, a := range item.Actions {
			out[key{action: a, arn: item.ResourceARN, ctxFP: fp}] = true
		}
	}
	return out
}
