package simulate

import (
	"context"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/Caleb-Kelly-25/preflight/internal/engine"
	"github.com/Caleb-Kelly-25/preflight/internal/finding"
)

// Client implements engine.Simulator against the real IAM policy simulator.
var _ engine.Simulator = (*Client)(nil)

// Options tunes the client. The defaults are deliberately conservative.
type Options struct {
	// StartConcurrency is where the adaptive limiter begins. Default 2.
	StartConcurrency int
	// MaxConcurrency is the ceiling it may climb to. Default 8. Not a measured
	// limit — IAM's rate limits are not published.
	MaxConcurrency int
	// MaxProduct caps len(actions) x len(arns) per call, so one call is
	// normally one page. Default 1000, the documented MaxItems maximum.
	MaxProduct int
	// MaxAttempts is handed to the SDK retryer. Default 5.
	MaxAttempts int
}

func (o Options) withDefaults() Options {
	if o.StartConcurrency <= 0 {
		o.StartConcurrency = 2
	}
	if o.MaxConcurrency <= 0 {
		o.MaxConcurrency = 8
	}
	if o.MaxProduct <= 0 {
		o.MaxProduct = 1000
	}
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = 5
	}
	return o
}

// Client batches, caches, paginates and retries on the engine's behalf.
type Client struct {
	iam   iamAPI
	sts   stsAPI
	opts  Options
	cache *cache
	stats *statsRecorder
}

// New builds a Client from an AWS config.
func New(cfg aws.Config, opts Options) *Client {
	return newWithAPIs(iam.NewFromConfig(cfg), sts.NewFromConfig(cfg), opts)
}

// newWithAPIs is the test constructor: it takes the narrow interfaces, so tests
// need no credentials and no network.
func newWithAPIs(i iamAPI, s stsAPI, opts Options) *Client {
	return &Client{
		iam:   i,
		sts:   s,
		opts:  opts.withDefaults(),
		cache: newCache(),
		stats: &statsRecorder{},
	}
}

// STS exposes the STS API for identity resolution.
func (c *Client) STS() stsAPI { return c.sts }

// IAM exposes the IAM API for the context-key probe.
func (c *Client) IAM() iamAPI { return c.iam }

// Resolve answers every question in one pass.
//
// The engine hands over the whole set precisely so that this method can group,
// dedupe, chunk, paginate and retry — none of which is possible when questions
// arrive one at a time.
//
// A Client may be reused across calls — the result cache is deliberately shared,
// since a decided (action, ARN, context) triple stays decided. Concurrent calls
// on one Client are not supported: the batches within a single Resolve run
// concurrently, but Resolve itself is not reentrant, and Stats would interleave.
func (c *Client) Resolve(ctx context.Context, req engine.Request) (engine.Response, error) {
	start := time.Now()

	// Stats and cache hits accumulate on the Client, so report the delta for
	// this call rather than the running total. A reused Client would otherwise
	// report each call's numbers inflated by every call before it.
	statsBefore := c.stats.snapshot()
	hitsBefore := c.cache.hitCount()

	// Every pair the request asks about, minus the ones already decided.
	wanted := wantedKeys(req.Items)
	for k := range wanted {
		if _, ok := c.cache.get(k); ok {
			delete(wanted, k)
			c.cache.countHit()
		}
	}

	batches := groupBatches(req.Items, wanted, c.opts.MaxProduct)

	warnings, err := c.runBatches(ctx, req.PolicySourceARN, batches)
	if err != nil {
		// Only a fatal error reaches here: a missing permission or a principal
		// that does not exist. Neither is a finding, and neither improves by
		// continuing.
		return engine.Response{}, err
	}

	resp := engine.Response{
		Outcomes: c.assemble(req.Items),
		Warnings: warnings,
	}
	resp.Stats = statsDelta(c.stats.snapshot(), statsBefore)
	resp.Stats.CacheHits = c.cache.hitCount() - hitsBefore
	resp.Stats.Elapsed = time.Since(start)
	return resp, nil
}

// statsDelta reports what one Resolve did, given the Client's running totals
// before and after.
func statsDelta(after, before engine.Stats) engine.Stats {
	return engine.Stats{
		Calls:       after.Calls - before.Calls,
		Evaluations: after.Evaluations - before.Evaluations,
		Pages:       after.Pages - before.Pages,
		Retries:     after.Retries - before.Retries,
		Throttles:   after.Throttles - before.Throttles,
	}
}

// runBatches executes the batches under the adaptive limiter, aborting the
// whole run only on a fatal error.
func (c *Client) runBatches(ctx context.Context, principalARN string, batches []batch) ([]string, error) {
	if len(batches) == 0 {
		return nil, nil
	}

	// A fatal error cancels the remaining work: forty more calls that will all
	// fail the same way waste the user's time and AWS's rate limit.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	lim := newLimiter(c.opts.StartConcurrency, c.opts.MaxConcurrency)

	var (
		mu       sync.Mutex
		warnings []string
		fatalErr error
		wg       sync.WaitGroup
	)

	for _, b := range batches {
		if ctx.Err() != nil {
			break
		}
		release, err := lim.acquire(ctx)
		if err != nil {
			break
		}

		wg.Add(1)
		go func(b batch) {
			defer wg.Done()

			_, err := c.runBatch(ctx, principalARN, b)
			throttled := classifyErr(err) == errBatchThrottled
			if throttled {
				c.stats.addThrottle()
			}
			release(throttled)

			if err == nil {
				return
			}

			mu.Lock()
			defer mu.Unlock()
			if f := fatal(err, principalARN, "iam:SimulatePrincipalPolicy"); f != nil {
				if fatalErr == nil {
					fatalErr = f
					cancel()
				}
				return
			}
			// Context cancellation caused by a fatal error elsewhere is not
			// itself worth reporting.
			if ctx.Err() == nil {
				warnings = append(warnings, describeBatchErr(err, b.arns))
			}
		}(b)
	}

	wg.Wait()
	if fatalErr != nil {
		return nil, fatalErr
	}
	return warnings, nil
}

// assemble reads the answers back out, index-aligned with the questions.
//
// A pair with no cached answer means its batch failed or its page was lost, so
// the item carries an error and the engine degrades that finding to Unchecked.
func (c *Client) assemble(items []engine.RequestItem) []engine.ItemOutcome {
	out := make([]engine.ItemOutcome, len(items))

	for i, item := range items {
		fp := finding.Fingerprint(item.Context)
		results := make([]finding.ActionResult, 0, len(item.Actions))
		var missing int

		for _, a := range item.Actions {
			r, ok := c.cache.get(key{action: a, arn: item.ResourceARN, ctxFP: fp})
			if !ok {
				missing++
				r = finding.ActionResult{
					Action:      a,
					ResourceARN: item.ResourceARN,
					Decision:    finding.DecisionNotSimulated,
				}
			}
			results = append(results, r)
		}

		out[i] = engine.ItemOutcome{Results: results}
		if missing > 0 {
			out[i].Err = errUnevaluated{count: missing, total: len(item.Actions)}
		}
	}
	return out
}

// errUnevaluated reports that some actions never got an answer.
type errUnevaluated struct{ count, total int }

func (e errUnevaluated) Error() string {
	return "some required actions were never evaluated"
}

// statsRecorder accumulates diagnostics across concurrent batches.
type statsRecorder struct {
	mu    sync.Mutex
	stats engine.Stats
}

func (s *statsRecorder) addCall() {
	s.mu.Lock()
	s.stats.Calls++
	s.mu.Unlock()
}

func (s *statsRecorder) addPage() {
	s.mu.Lock()
	s.stats.Pages++
	s.mu.Unlock()
}

func (s *statsRecorder) addThrottle() {
	s.mu.Lock()
	s.stats.Throttles++
	s.mu.Unlock()
}

func (s *statsRecorder) addEvaluations(n int) {
	s.mu.Lock()
	s.stats.Evaluations += n
	s.mu.Unlock()
}

func (s *statsRecorder) snapshot() engine.Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}
