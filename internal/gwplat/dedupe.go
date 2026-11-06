package gwplat

import (
	"sync"
	"time"
)

// Dedupe constants.
const (
	DefaultDedupeSize = 1024
	DefaultDedupeTTL  = 30 * time.Minute
)

// Dedupe is the replay window every adapter keeps for inbound ids (NIP-01
// event ids, IRCv3 msgid, signal timestamps, daemon message ids). Claim
// returns true exactly once per key within TTL; a replayed frame never becomes
// a second turn.
type Dedupe struct {
	mu   sync.Mutex
	ttl  time.Duration
	max  int
	seen map[string]time.Time
}

// NewDedupe returns a window bounded in entries and time. Non-positive values
// take the defaults; a bounded window never grows with traffic.
func NewDedupe(max int, ttl time.Duration) *Dedupe {
	if max <= 0 {
		max = DefaultDedupeSize
	}
	if ttl <= 0 {
		ttl = DefaultDedupeTTL
	}
	return &Dedupe{ttl: ttl, max: max, seen: make(map[string]time.Time, max)}
}

// Claim reports whether key is new at at. An empty key cannot be deduped and
// is accepted: the adapter must synthesize an id before calling Claim.
func (d *Dedupe) Claim(key string, at time.Time) bool {
	if key == "" {
		return true
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if seen, ok := d.seen[key]; ok && at.Sub(seen) < d.ttl {
		return false
	}
	d.seen[key] = at
	if len(d.seen) > d.max {
		d.evictLocked(at)
	}
	return true
}

// Len reports the current window size.
func (d *Dedupe) Len() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.seen)
}

func (d *Dedupe) evictLocked(at time.Time) {
	for key, seen := range d.seen {
		if at.Sub(seen) >= d.ttl {
			delete(d.seen, key)
		}
	}
	for len(d.seen) > d.max {
		var oldestKey string
		var oldest time.Time
		for key, seen := range d.seen {
			if oldestKey == "" || seen.Before(oldest) {
				oldestKey, oldest = key, seen
			}
		}
		delete(d.seen, oldestKey)
	}
}
