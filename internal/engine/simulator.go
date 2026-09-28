package engine

import (
	"context"
	"time"

	"github.com/Caleb-Kelly-25/preflight/internal/finding"
)

// Simulator answers a whole run's worth of IAM questions in one pass.
//
// The shape matters. The obvious interface — evaluate these actions against
// this one ARN — cannot batch: by the time the first call arrives the
// implementation has seen one resource, so it cannot group by action set,
// dedupe (action, ARN) pairs across resources, or size a chunk. All three need
// the whole question set up front. An earlier version of this package had that
// shape, and docs/DESIGN.md §9.5's claim that "batching lives behind it" was
// simply false.
//
// So the engine collects every question before any of them is asked, and the
// implementation is free to group, dedupe, chunk, paginate and retry however it
// likes. The engine still performs no I/O: Resolve is one interface call.
type Simulator interface {
	Resolve(ctx context.Context, req Request) (Response, error)
}

// Request is everything one run needs decided. Built entirely by the engine's
// collect phase from the plan and the mapping database, with no network access.
type Request struct {
	// PolicySourceARN is the principal to evaluate against.
	PolicySourceARN string
	// Items are the questions, in plan order.
	Items []RequestItem
}

// RequestItem is one (resource change × operation): an action set evaluated
// against one resource ARN under one context.
type RequestItem struct {
	// Actions is the required IAM action list, deduped and ordered by the
	// engine so that identical sets hash identically.
	Actions []string
	// ResourceARN is what to scope the evaluation to. "*" when the plan does
	// not determine it — which makes any resulting denial inconclusive.
	ResourceARN string
	// Context is the condition-key values to supply, pre-sorted so that
	// finding.Fingerprint is stable. Items may only share an API call when
	// their fingerprints match: IAM applies ContextEntries per call, not per
	// resource.
	Context []finding.ContextEntry
}

// Response answers a Request.
type Response struct {
	// Outcomes MUST be the same length as Request.Items and index-aligned with
	// it: Outcomes[i] answers Items[i].
	//
	// Index alignment rather than a lookup keyed on (action, ARN, context) is a
	// deliberate choice. It keeps test fakes to a few lines, it avoids needing
	// a []ContextEntry as a map key, and it makes report ordering deterministic
	// under concurrency for free — whichever batch finishes first, outcome i is
	// still finding i.
	Outcomes []ItemOutcome

	// Warnings are run-level caveats not tied to one resource: batches that
	// were throttled past their retries, pages that were lost.
	Warnings []string

	// Stats is diagnostic only, surfaced under --explain and in JSON output.
	// IAM's rate limits are not published, so this is how we learn the real ones
	// from the field — which only works if the numbers reach a user. They did not
	// until 2026-09-28: Options.resolve copied Warnings and Outcomes and dropped
	// these, so the promise in this comment was false for the whole of M2.
	Stats Stats
}

// ItemOutcome is the result for one RequestItem.
type ItemOutcome struct {
	// Results has one entry per RequestItem.Actions entry, in the same order.
	// An action that came back with no result carries DecisionNotSimulated.
	Results []finding.ActionResult

	// Err is set when any action in this item went unevaluated — the batch was
	// throttled past its retries, the connection failed, or pagination stopped
	// early. The engine degrades the whole finding to Unchecked; it never
	// treats a partial answer as a pass.
	Err error
}

// Stats records what the simulator actually did.
type Stats struct {
	// Calls is API calls issued; Evaluations is action×resource pairs returned.
	// The ratio is how batching effectiveness is measured — if per-resource
	// condition-key values fragment batches, Calls climbs toward Evaluations.
	Calls       int
	Evaluations int
	Pages       int
	Retries     int
	Throttles   int
	CacheHits   int
	Elapsed     time.Duration
}
