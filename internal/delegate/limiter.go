package delegate

import (
	"sync"
	"time"
)

// limiter is the per-session fixed-rate gate used for two expensive-tool
// decisions: allow at most one delegate_task per MinCallPeriod, and deliver at
// most one rate-limit notice per period (TOOL-05/TOOL-08). It is the same
// token-bucket semantics as gateway.Limiter, kept local because gateway is a
// frontend layer this package must not import (E-01).
type limiter struct {
	period time.Duration
	mu     sync.Mutex
	next   map[string]time.Time
}

// take reserves key at now. ok=false returns the remaining wait and reserves
// nothing, so a denied caller cannot extend its own penalty.
func (l *limiter) take(key string, now time.Time) (ok bool, wait time.Duration) {
	if l.period <= 0 || key == "" {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.next == nil {
		l.next = map[string]time.Time{}
	}
	if at, seen := l.next[key]; seen && now.Before(at) {
		return false, at.Sub(now)
	}
	l.next[key] = now.Add(l.period)
	return true, 0
}
