package simulate

import (
	"sync"

	"github.com/Caleb-Kelly-25/preflight/internal/finding"
)

// cache holds decided questions for the life of one Resolve call.
//
// Worth having even within a single run: a replacement produces create and
// delete against the same ARN, and a module instantiated N times repeats the
// same (action, ARN) pair N times. Mutex-guarded because batches run
// concurrently.
type cache struct {
	mu sync.Mutex
	m  map[key]finding.ActionResult
	// hits counts pairs served without an API call, for Stats.
	hits int
}

func newCache() *cache {
	return &cache{m: make(map[key]finding.ActionResult)}
}

func (c *cache) get(k key) (finding.ActionResult, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.m[k]
	return r, ok
}

// countHit records a pair served from cache rather than asked about.
func (c *cache) countHit() {
	c.mu.Lock()
	c.hits++
	c.mu.Unlock()
}

func (c *cache) hitCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits
}

// put stores one decided pair. Everything AWS returns is stored, not only the
// pairs we asked about: the response is a cartesian product, so the surplus
// combinations are already paid for and may answer a later question for free.
func (c *cache) put(k key, r finding.ActionResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.m[k]; exists {
		return
	}
	c.m[k] = r
}

func (c *cache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.m)
}
