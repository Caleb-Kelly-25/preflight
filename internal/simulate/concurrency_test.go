package simulate

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/Caleb-Kelly-25/preflight/internal/engine"
	"github.com/Caleb-Kelly-25/preflight/internal/finding"
)

// These tests exercise the concurrent paths — the shared cache, the shared
// stats recorder, and the AIMD limiter.
//
// They are not a substitute for `go test -race`, which is where a data race is
// actually proven. They are the next best thing: enough concurrent work that a
// broken lock shows up as a wrong or missing ANSWER, which is detectable without
// the race detector. Run both; CI runs -race on Linux.

// TestConcurrentBatchesProduceCorrectAnswers pushes many batches through the
// limiter at once and checks every single answer.
//
// A lost cache write, a torn map read, or a mismatched outcome index all surface
// here as a wrong decision or an unevaluated item, rather than as a silent
// corruption that only shows up in production.
func TestConcurrentBatchesProduceCorrectAnswers(t *testing.T) {
	const resources = 200

	// Deny every third action so a mix-up between resources is visible rather
	// than uniform.
	deny := map[string]bool{"s3:DeleteBucket": true}
	f := &fakeIAM{deny: deny}

	// One ARN per call maximises contention: 200 concurrent-ish batches.
	c := newWithAPIs(f, &fakeSTS{}, Options{
		MaxProduct: 1, StartConcurrency: 8, MaxConcurrency: 16,
	})

	var items []engine.RequestItem
	for i := 0; i < resources; i++ {
		items = append(items, engine.RequestItem{
			Actions:     []string{"s3:CreateBucket", "s3:DeleteBucket"},
			ResourceARN: fmt.Sprintf("arn:aws:s3:::bucket-%03d", i),
		})
	}

	resp, err := c.Resolve(context.Background(), engine.Request{
		PolicySourceARN: "arn:aws:iam::1:role/r", Items: items,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if len(resp.Outcomes) != resources {
		t.Fatalf("got %d outcomes for %d items", len(resp.Outcomes), resources)
	}
	for i, o := range resp.Outcomes {
		if o.Err != nil {
			t.Fatalf("item %d unevaluated under concurrency: %v", i, o.Err)
		}
		if len(o.Results) != 2 {
			t.Fatalf("item %d has %d results, want 2", i, len(o.Results))
		}
		// Index alignment must hold regardless of which batch finished first.
		wantARN := fmt.Sprintf("arn:aws:s3:::bucket-%03d", i)
		for _, r := range o.Results {
			if r.ResourceARN != wantARN {
				t.Fatalf("item %d answered with %q — outcomes are misaligned", i, r.ResourceARN)
			}
			want := finding.DecisionAllowed
			if deny[r.Action] {
				want = finding.DecisionImplicitDeny
			}
			if r.Decision != want {
				t.Fatalf("item %d %s = %q, want %q", i, r.Action, r.Decision, want)
			}
		}
	}

	if resp.Stats.Calls != resources {
		t.Errorf("Stats.Calls = %d, want %d — the counter dropped increments under contention",
			resp.Stats.Calls, resources)
	}
	if resp.Stats.Evaluations != resources*2 {
		t.Errorf("Stats.Evaluations = %d, want %d", resp.Stats.Evaluations, resources*2)
	}
}

// TestLimiterRespectsCeiling — the limiter is the one piece of hand-rolled
// synchronisation in the codebase, so its invariant is asserted directly.
func TestLimiterRespectsCeiling(t *testing.T) {
	const ceiling = 4
	lim := newLimiter(ceiling, ceiling)

	var (
		mu       sync.Mutex
		inFlight int
		peak     int
		wg       sync.WaitGroup
	)

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := lim.acquire(context.Background())
			if err != nil {
				t.Errorf("acquire: %v", err)
				return
			}
			mu.Lock()
			inFlight++
			if inFlight > peak {
				peak = inFlight
			}
			mu.Unlock()

			mu.Lock()
			inFlight--
			mu.Unlock()
			release(false)
		}()
	}
	wg.Wait()

	if peak > ceiling {
		t.Errorf("peak concurrency %d exceeded the ceiling of %d", peak, ceiling)
	}
}

// TestLimiterBacksOffAndRecovers pins the AIMD behaviour: halve on throttling,
// climb back after a clean run. Nothing here is a measured AWS limit — the point
// is that the controller reacts at all.
func TestLimiterBacksOffAndRecovers(t *testing.T) {
	lim := newLimiter(8, 8)

	release, err := lim.acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	release(true) // throttled
	if got := lim.current(); got != 4 {
		t.Errorf("limit = %d after a throttle, want it halved to 4", got)
	}

	for i := 0; i < recoverAfter; i++ {
		r, err := lim.acquire(context.Background())
		if err != nil {
			t.Fatalf("acquire: %v", err)
		}
		r(false)
	}
	if got := lim.current(); got != 5 {
		t.Errorf("limit = %d after %d clean calls, want 5", got, recoverAfter)
	}
}

// TestLimiterUnblocksOnCancel — a cancelled run must not leave goroutines parked
// on the condition variable. If this regresses the symptom is a CI job that
// hangs, which is the worst failure mode available to us.
func TestLimiterUnblocksOnCancel(t *testing.T) {
	lim := newLimiter(1, 1)

	held, err := lim.acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	blocked := make(chan error, 1)
	go func() {
		_, err := lim.acquire(ctx) // no slot free; this parks
		blocked <- err
	}()

	cancel()

	select {
	case err := <-blocked:
		if err == nil {
			t.Error("acquire returned a slot after its context was cancelled")
		}
	case <-ctx.Done():
		select {
		case err := <-blocked:
			if err == nil {
				t.Error("acquire returned a slot after its context was cancelled")
			}
		}
	}
	held(false)
}

// TestStatsArePerCallNotCumulative — a Client may be reused, and a metric that
// silently includes every previous call is worse than no metric.
func TestStatsArePerCallNotCumulative(t *testing.T) {
	f := &fakeIAM{}
	c := newWithAPIs(f, &fakeSTS{}, Options{})

	first := engine.Request{PolicySourceARN: "p",
		Items: []engine.RequestItem{item("arn:aws:s3:::a", "s3:CreateBucket")}}
	second := engine.Request{PolicySourceARN: "p",
		Items: []engine.RequestItem{item("arn:aws:s3:::b", "s3:CreateBucket")}}

	if _, err := c.Resolve(context.Background(), first); err != nil {
		t.Fatalf("first Resolve: %v", err)
	}
	resp, err := c.Resolve(context.Background(), second)
	if err != nil {
		t.Fatalf("second Resolve: %v", err)
	}

	if resp.Stats.Calls != 1 {
		t.Errorf("second call reported Calls = %d, want 1 (not the running total)", resp.Stats.Calls)
	}
	if resp.Stats.Evaluations != 1 {
		t.Errorf("second call reported Evaluations = %d, want 1", resp.Stats.Evaluations)
	}
}

// TestCacheSurvivesReuse — the cache is shared across Resolve calls on purpose,
// since a decided triple stays decided.
func TestCacheSurvivesReuse(t *testing.T) {
	f := &fakeIAM{}
	c := newWithAPIs(f, &fakeSTS{}, Options{})

	req := engine.Request{PolicySourceARN: "p",
		Items: []engine.RequestItem{item("arn:aws:s3:::a", "s3:CreateBucket")}}

	if _, err := c.Resolve(context.Background(), req); err != nil {
		t.Fatalf("first Resolve: %v", err)
	}
	resp, err := c.Resolve(context.Background(), req)
	if err != nil {
		t.Fatalf("second Resolve: %v", err)
	}

	if f.callCount() != 1 {
		t.Errorf("made %d API calls for the same question twice, want 1", f.callCount())
	}
	if resp.Outcomes[0].Err != nil {
		t.Errorf("cached answer came back unevaluated: %v", resp.Outcomes[0].Err)
	}
	if resp.Stats.CacheHits != 1 {
		t.Errorf("Stats.CacheHits = %d, want 1", resp.Stats.CacheHits)
	}
}
