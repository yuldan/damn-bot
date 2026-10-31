package delegate

import (
	"sync"
	"time"
)

// limiter is the per-session fixed-rate gate for delegate_task and its notice
// (TOOL-05/TOOL-08). Same semantics as gateway.Limiter, kept local (E-01).
type limiter struct {
	period time.Duration
	mu     sync.Mutex
	next   map[string]time.Time
}

// take reserves key at now; a denial returns the wait and reserves nothing.
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
