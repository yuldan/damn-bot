//go:build !minimal

package exec

import (
	"sync"
	"time"
)

// Notice limits of one process (EXEC-AND-CODING §A.5, GAP-TOOL-08): at most
// one notice per 15 s per process, four per minute per chat, and fifteen per
// ten seconds globally. Excess notifications are merged into the next allowed
// one and reported as +N.
const (
	NoticeProcessEvery = 15 * time.Second
	NoticeChatLimit    = 4
	NoticeChatWindow   = time.Minute
	NoticeGlobalLimit  = 15
	NoticeGlobalWindow = 10 * time.Second
)

// NoticeLimiter is a concurrency-safe rate gate for process-exit notices.
// Offer returns whether this notice may be delivered and how many suppressed
// notices it merges. State lives in the value (E-10).
type NoticeLimiter struct {
	mu      sync.Mutex
	proc    map[string]time.Time
	pending map[string]int
	chat    map[string]*noticeBucket
	global  noticeBucket
}

type noticeBucket struct {
	left float64
	last time.Time
}

// NewNoticeLimiter returns a limiter with the §A.5 defaults.
func NewNoticeLimiter() *NoticeLimiter {
	return &NoticeLimiter{
		proc: map[string]time.Time{}, pending: map[string]int{},
		chat:   map[string]*noticeBucket{},
		global: noticeBucket{left: NoticeGlobalLimit},
	}
}

// Offer reports whether a notice for process id in chat may be sent at now.
// A denied offer increments the merge counter; the next allowed offer returns
// it as merged and resets the counter.
func (l *NoticeLimiter) Offer(id, chat string, now time.Time) (send bool, merged int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if last, seen := l.proc[id]; seen && now.Sub(last) < NoticeProcessEvery {
		l.pending[id]++
		return false, 0
	}
	cb := l.chat[chat]
	if cb == nil {
		cb = &noticeBucket{left: NoticeChatLimit, last: now}
		l.chat[chat] = cb
	}
	refill(cb, now, NoticeChatLimit, NoticeChatWindow)
	refill(&l.global, now, NoticeGlobalLimit, NoticeGlobalWindow)
	if cb.left < 1 || l.global.left < 1 {
		l.pending[id]++
		return false, 0
	}
	cb.left--
	l.global.left--
	l.proc[id] = now
	merged = l.pending[id]
	delete(l.pending, id)
	return true, merged
}

func refill(b *noticeBucket, now time.Time, limit int, window time.Duration) {
	if b.last.IsZero() {
		b.last, b.left = now, float64(limit)
		return
	}
	if elapsed := now.Sub(b.last); elapsed > 0 {
		b.left += float64(limit) * float64(elapsed) / float64(window)
		if b.left > float64(limit) {
			b.left = float64(limit)
		}
		b.last = now
	}
}
