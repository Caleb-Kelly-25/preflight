package simulate

import (
	"context"
	"sync"
)

// limiter is an AIMD controller over in-flight SimulatePrincipalPolicy calls.
//
// Nothing here is a measured number. IAM's per-action rate limits are not
// published, so the design's rule is to start conservatively, react to what AWS
// actually says, and record the outcome rather than guess a ceiling. It starts
// at 2 (docs/DESIGN.md §9.3 — the one number the design sanctions), halves on
// throttling, and recovers by one after a run of clean calls.
//
// Per-call backoff and jitter are the SDK retryer's job; this only governs how
// many calls are in flight at once.
type limiter struct {
	mu       sync.Mutex
	cond     *sync.Cond
	limit    int
	maxLimit int
	inFlight int
	clean    int
	closed   bool
}

// recoverAfter is how many consecutive clean releases earn one extra slot.
const recoverAfter = 8

func newLimiter(start, maxLimit int) *limiter {
	if start < 1 {
		start = 1
	}
	if maxLimit < start {
		maxLimit = start
	}
	l := &limiter{limit: start, maxLimit: maxLimit}
	l.cond = sync.NewCond(&l.mu)
	return l
}

// acquire blocks until a slot is free. The returned release must be called
// exactly once, reporting whether the call was throttled.
func (l *limiter) acquire(ctx context.Context) (func(throttled bool), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Wake any waiter if the context is cancelled while it is parked, so a
	// cancelled run does not hang on a condition variable.
	stop := context.AfterFunc(ctx, func() {
		l.mu.Lock()
		l.closed = true
		l.mu.Unlock()
		l.cond.Broadcast()
	})

	l.mu.Lock()
	for l.inFlight >= l.limit && !l.closed {
		l.cond.Wait()
	}
	if l.closed {
		l.mu.Unlock()
		stop()
		return nil, ctx.Err()
	}
	l.inFlight++
	l.mu.Unlock()

	var once sync.Once
	return func(throttled bool) {
		once.Do(func() {
			stop()
			l.release(throttled)
		})
	}, nil
}

func (l *limiter) release(throttled bool) {
	l.mu.Lock()
	l.inFlight--
	if throttled {
		l.clean = 0
		if l.limit > 1 {
			l.limit /= 2
		}
	} else {
		l.clean++
		if l.clean >= recoverAfter && l.limit < l.maxLimit {
			l.limit++
			l.clean = 0
		}
	}
	l.mu.Unlock()
	l.cond.Broadcast()
}

func (l *limiter) current() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.limit
}
