package outq_test

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/gateway/outq"
)

// fakePacer models a token bucket with one interval per chat and no global
// limit, so the scheduler's pacing can be observed without waiting seconds.
type fakePacer struct {
	mu       sync.Mutex
	interval time.Duration
	next     map[string]time.Time
	penalty  map[string]time.Time
	taken    map[string]int
	held     bool
	asked    chan struct{}
}

func newFakePacer(interval time.Duration) *fakePacer {
	return &fakePacer{interval: interval, next: map[string]time.Time{}, penalty: map[string]time.Time{}, taken: map[string]int{}, asked: make(chan struct{}, 1)}
}

// hold closes the permit gate: TakeChat keeps refusing, so the dispatcher can
// never hand a part to a worker. release opens it. The gate is the test's only
// ordering control, which keeps the assertions independent of goroutine timing.
func (p *fakePacer) hold() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.held = true
}

func (p *fakePacer) release() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.held = false
}

// waitAsk blocks until the dispatcher has consulted the pacer at least once.
// It reports a control signal, not elapsed time.
func (p *fakePacer) waitAsk(t *testing.T) {
	t.Helper()
	select {
	case <-p.asked:
	case <-time.After(3 * time.Second):
		t.Fatal("dispatcher never asked the pacer")
	}
}

func (p *fakePacer) TakeChat(_, chat string, _ bool, at time.Time) (bool, time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case p.asked <- struct{}{}:
	default:
	}
	if p.held {
		return false, 10 * time.Millisecond
	}
	if until := p.penalty[chat]; until.After(at) {
		return false, until.Sub(at)
	}
	next := p.next[chat]
	if at.Before(next) {
		return false, next.Sub(at)
	}
	p.next[chat] = at.Add(p.interval)
	p.taken[chat]++
	return true, 0
}

func (p *fakePacer) Penalize(chat string, until time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.penalty[chat] = until
}

func (p *fakePacer) penalized(chat string) time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.penalty[chat]
}

type recorder struct {
	mu    sync.Mutex
	order []string
	sends map[string][]string
	times map[string][]time.Time
	fail  map[string]error
}

func newRecorder() *recorder {
	return &recorder{sends: map[string][]string{}, times: map[string][]time.Time{}, fail: map[string]error{}}
}

func (r *recorder) send(ctx context.Context, chat string, part outq.Part) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("test send: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.order = append(r.order, chat)
	r.sends[chat] = append(r.sends[chat], part.Text)
	r.times[chat] = append(r.times[chat], time.Now())
	if err := r.fail[part.Text]; err != nil {
		return err
	}
	return nil
}

func (r *recorder) snapshot() ([]string, map[string][]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string][]string{}
	for k, v := range r.sends {
		out[k] = append([]string(nil), v...)
	}
	return append([]string(nil), r.order...), out
}

func runScheduler(t *testing.T, s *outq.Scheduler) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		s.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("scheduler did not stop")
		}
	})
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	waitForWithin(t, 3*time.Second, what, cond)
}

// waitForWithin polls cond until it holds or timeout elapses. The timeout is a
// hang guard, never a throughput budget: callers assert progress through
// signals, so a slow runner costs latency instead of a failure.
func waitForWithin(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("timeout waiting for %s", what)
}

func parts(n int) []outq.Part {
	out := make([]outq.Part, n)
	for i := range out {
		out[i] = outq.Part{Text: "p" + string(rune('a'+i)), Index: i}
	}
	return out
}

// replayHangGuard bounds the replay test only to fail a deadlock; it is far
// above any expected runtime, including a Windows -race runner, and no
// assertion depends on how fast the 200 sends complete.
const replayHangGuard = 60 * time.Second

// replayLog collects the per-send handshake of the replay test and the reply
// text of every send, so exactly-once delivery can be asserted without a
// throughput deadline.
type replayLog struct {
	mu   sync.Mutex
	seen map[string]int
	sent chan struct{}
}

func newReplayLog(total int) *replayLog {
	return &replayLog{seen: make(map[string]int, total), sent: make(chan struct{}, total)}
}

// record is the adapter-facing Send: it forwards to the recorder and signals
// the test. The signal is non-blocking so a duplicate can never wedge a worker.
func (l *replayLog) record(ctx context.Context, rec *recorder, part outq.Part) error {
	if err := rec.send(ctx, "-100", part); err != nil {
		return err
	}
	l.mu.Lock()
	l.seen[part.Text]++
	l.mu.Unlock()
	select {
	case l.sent <- struct{}{}:
	default:
	}
	return nil
}

// await blocks until every expected send has handshaked. The timer is a hang
// guard, not a budget: progress is signalled, so a slow runner only adds
// latency.
func (l *replayLog) await(t *testing.T, total int) {
	t.Helper()
	guard := time.NewTimer(replayHangGuard)
	defer guard.Stop()
	for range total {
		select {
		case <-l.sent:
		case <-guard.C:
			t.Fatalf("hang guard %s: %d of %d replies sent", replayHangGuard, total-len(l.sent), total)
		}
	}
}

// duplicated reports how many distinct replies were sent and how many sends
// repeated an already-sent reply.
func (l *replayLog) duplicated() (unique, dup int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	unique = len(l.seen)
	for _, n := range l.seen {
		dup += n - 1
	}
	return unique, dup
}

// TestF7GAPGW03OutqReplay replays 200 replies to one group under the official
// Telegram structure (one token per part, group bucket first) and asserts the
// fake server never answers 429: the scheduler, not the adapter, paces.
// Completion is signalled per send, so replay is asserted exactly-once instead
// of racing a fixed deadline.
func TestF7GAPGW03OutqReplay(t *testing.T) {
	pacer := newFakePacer(time.Millisecond)
	rec := newRecorder()
	s, err := outq.New(&outq.Options{Pacer: pacer, Workers: 2, MaxChunksPerReply: 5})
	if err != nil {
		t.Fatal(err)
	}
	runScheduler(t, s)
	const total = 200
	log := newReplayLog(total)
	for i := range total {
		text := fmt.Sprintf("replay-%03d", i)
		msg := &outq.Message{
			Platform: "telegram", Chat: "-100", Group: true, Key: text,
			Parts: []outq.Part{{Text: text}},
			Send:  func(ctx context.Context, p outq.Part) error { return log.record(ctx, rec, p) },
			Overflow: func(context.Context, string) error {
				t.Error("unexpected overflow for a one-part reply")
				return nil
			},
			RetryAfter: func(error) (time.Duration, bool) { return 0, false },
		}
		if err := s.Enqueue(t.Context(), msg); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}
	log.await(t, total)
	waitForWithin(t, replayHangGuard, "scheduler to drain", func() bool {
		st := s.Stats()
		return st.Sent == total && st.Queued == 0 && st.InFlight == 0
	})
	if unique, dup := log.duplicated(); unique != total || dup != 0 {
		t.Fatalf("replay sent %d unique replies (%d duplicated), want %d unique and none repeated", unique, dup, total)
	}
	if _, sends := rec.snapshot(); len(sends["-100"]) != total {
		t.Fatalf("recorded sends = %d, want %d", len(sends["-100"]), total)
	}
	if st := s.Stats(); st.Failed != 0 || st.Throttled == 0 {
		t.Fatalf("stats = %+v, want 0 failed and observed throttling", st)
	}
}

// TestF7GAPGW03ChunkCap proves MaxChunksPerReply: the sixth part and beyond
// travel through one overflow attachment instead of extra chat messages.
func TestF7GAPGW03ChunkCap(t *testing.T) {
	rec := newRecorder()
	overflow := make(chan string, 1)
	s, err := outq.New(&outq.Options{Pacer: newFakePacer(0), Workers: 1, MaxChunksPerReply: 5})
	if err != nil {
		t.Fatal(err)
	}
	runScheduler(t, s)
	msg := &outq.Message{
		Platform: "telegram", Chat: "c1", Parts: parts(9),
		Send:     func(ctx context.Context, p outq.Part) error { return rec.send(ctx, "c1", p) },
		Overflow: func(_ context.Context, body string) error { overflow <- body; return nil },
	}
	if err := s.Enqueue(t.Context(), msg); err != nil {
		t.Fatal(err)
	}
	var body string
	select {
	case body = <-overflow:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for overflow")
	}
	if _, sends := rec.snapshot(); len(sends["c1"]) != 5 {
		t.Fatalf("chunks = %d, want 5", len(sends["c1"]))
	}
	if want := strings.Join([]string{"pf", "pg", "ph", "pi"}, "\n\n"); body != want {
		t.Fatalf("overflow = %q, want %q", body, want)
	}
}

// TestF7GAPGW03OverflowRequired is the negative contract: an adapter that
// declares no attachment path cannot silently drop the tail.
func TestF7GAPGW03OverflowRequired(t *testing.T) {
	s, err := outq.New(&outq.Options{Pacer: newFakePacer(0), MaxChunksPerReply: 2})
	if err != nil {
		t.Fatal(err)
	}
	msg := &outq.Message{Platform: "slack", Chat: "c", Parts: parts(3), Send: func(context.Context, outq.Part) error { return nil }}
	if err := s.Enqueue(t.Context(), msg); !errors.Is(err, outq.ErrOverflowNeeded) {
		t.Fatalf("enqueue = %v, want ErrOverflowNeeded", err)
	}
}

// TestF7GAPGW03Fairness pins the round-robin invariant: while two chats of
// equal priority stay backlogged, parts alternate in chat-activation order
// (a,b,a,b,...). Every part is enqueued before Run starts, so the dispatcher
// sees a fixed ready set and the assertion cannot depend on goroutine timing.
func TestF7GAPGW03Fairness(t *testing.T) {
	for _, tc := range []struct {
		name  string
		chats []string
		parts []int
		want  []string
	}{
		{"ab", []string{"a", "b"}, []int{3, 3}, []string{"a", "b", "a", "b", "a", "b"}},
		{"ba", []string{"b", "a"}, []int{3, 3}, []string{"b", "a", "b", "a", "b", "a"}},
		{"uneven", []string{"a", "b"}, []int{4, 2}, []string{"a", "b", "a", "b", "a", "a"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := newRecorder()
			s, err := outq.New(&outq.Options{Pacer: newFakePacer(0), Workers: 1})
			if err != nil {
				t.Fatal(err)
			}
			for i, chat := range tc.chats {
				for range tc.parts[i] {
					if err := s.Enqueue(t.Context(), &outq.Message{
						Platform: "telegram", Chat: chat, Parts: parts(1),
						Send: func(ctx context.Context, p outq.Part) error { return rec.send(ctx, chat, p) },
					}); err != nil {
						t.Fatal(err)
					}
				}
			}
			runScheduler(t, s)
			waitFor(t, "all sends", func() bool { order, _ := rec.snapshot(); return len(order) == len(tc.want) })
			order, _ := rec.snapshot()
			if !slices.Equal(order, tc.want) {
				t.Fatalf("order = %v, want %v", order, tc.want)
			}
		})
	}
}

// TestF7GAPGW03Priority serves a high-priority approval before queued chat
// traffic. Both replies are enqueued before Run starts, so priority describes
// a ready pair; the negative control is the reversed enqueue order, which must
// yield the same dispatch order.
func TestF7GAPGW03Priority(t *testing.T) {
	for _, enqueue := range [][]string{{"low", "high"}, {"high", "low"}} {
		t.Run(strings.Join(enqueue, "-"), func(t *testing.T) {
			rec := newRecorder()
			s, err := outq.New(&outq.Options{Pacer: newFakePacer(0), Workers: 1})
			if err != nil {
				t.Fatal(err)
			}
			priority := map[string]outq.Priority{"low": outq.PriorityLow, "high": outq.PriorityHigh}
			for _, chat := range enqueue {
				pri := priority[chat]
				if err := s.Enqueue(t.Context(), &outq.Message{
					Platform: "telegram", Chat: chat, Priority: pri, Parts: parts(1),
					Send: func(ctx context.Context, p outq.Part) error { return rec.send(ctx, chat, p) },
				}); err != nil {
					t.Fatal(err)
				}
			}
			runScheduler(t, s)
			waitFor(t, "two sends", func() bool { order, _ := rec.snapshot(); return len(order) == 2 })
			order, _ := rec.snapshot()
			if want := []string{"high", "low"}; !slices.Equal(order, want) {
				t.Fatalf("order = %v, want %v", order, want)
			}
		})
	}
}

// TestF7GAPGW03PermitGate is the negative control for pacing: while the pacer
// refuses, the dispatcher may ask but nothing is transmitted; the first send
// happens only after the gate opens.
func TestF7GAPGW03PermitGate(t *testing.T) {
	pacer := newFakePacer(0)
	pacer.hold()
	rec := newRecorder()
	s, err := outq.New(&outq.Options{Pacer: pacer, Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, chat := range []string{"high", "a", "b"} {
		if err := s.Enqueue(t.Context(), &outq.Message{
			Platform: "telegram", Chat: chat, Priority: outq.PriorityHigh, Parts: parts(1),
			Send: func(ctx context.Context, p outq.Part) error { return rec.send(ctx, chat, p) },
		}); err != nil {
			t.Fatal(err)
		}
	}
	runScheduler(t, s)
	for range 3 {
		pacer.waitAsk(t)
	}
	if order, _ := rec.snapshot(); len(order) != 0 {
		t.Fatalf("order = %v, want no dispatch while the permit is refused", order)
	}
	pacer.release()
	waitFor(t, "sends after release", func() bool { order, _ := rec.snapshot(); return len(order) == 3 })
	order, _ := rec.snapshot()
	// Order is pinned by the Priority and Fairness tests, which enqueue with
	// the permit open; a mid-round release may start the ring at any chat, so
	// this control asserts only that nothing was lost or duplicated.
	slices.Sort(order)
	if want := []string{"a", "b", "high"}; !slices.Equal(order, want) {
		t.Fatalf("sent = %v, want each queued part exactly once: %v", order, want)
	}
}

// TestF7GAPGW03RetryAfter proves a platform retry_after is consumed once by
// the scheduler, penalizes only that chat, and delays the retry.
func TestF7GAPGW03RetryAfter(t *testing.T) {
	pacer := newFakePacer(0)
	rec := newRecorder()
	var failOnce sync.Once
	boom := errors.New("telegram: 429")
	var attempt []time.Time
	var mu sync.Mutex
	s, err := outq.New(&outq.Options{Pacer: pacer, Workers: 1, RetryBase: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	runScheduler(t, s)
	msg := &outq.Message{
		Platform: "telegram", Chat: "c1", Parts: parts(1),
		Send: func(ctx context.Context, p outq.Part) error {
			mu.Lock()
			attempt = append(attempt, time.Now())
			mu.Unlock()
			failed := false
			failOnce.Do(func() { failed = true })
			if failed {
				return boom
			}
			return rec.send(ctx, "c1", p)
		},
		RetryAfter: func(error) (time.Duration, bool) { return 20 * time.Millisecond, true },
	}
	if err := s.Enqueue(t.Context(), msg); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "retry", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(attempt) == 2
	})
	mu.Lock()
	if got := attempt[1].Sub(attempt[0]); got < 15*time.Millisecond {
		t.Errorf("retry after %s, want >= 20ms (Retry-After honored)", got)
	}
	mu.Unlock()
	if pacer.penalized("c1").IsZero() {
		t.Error("retry_after did not penalize the chat")
	}
	waitFor(t, "send after retry", func() bool { return s.Stats().Sent == 1 })
	if st := s.Stats(); st.Retried != 1 || st.Sent != 1 {
		t.Fatalf("stats = %+v, want one retry then one send", st)
	}
}

// TestF7GAPGW03Cancel proves a cancelled scheduler stops without sending the
// rest and refuses new work after Close.
func TestF7GAPGW03Cancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	s, err := outq.New(&outq.Options{Pacer: newFakePacer(0), Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return on cancel")
	}
	s.Close()
	if err := s.Enqueue(t.Context(), &outq.Message{
		Platform: "x", Chat: "c", Parts: parts(1),
		Send: func(context.Context, outq.Part) error { return nil },
	}); !errors.Is(err, outq.ErrClosed) {
		t.Fatalf("enqueue after cancel = %v, want ErrClosed", err)
	}
}

// TestOutqPermitRejectsDirectCall is the negative contract: an adapter path
// that did not receive a scheduler permit cannot transmit.
func TestOutqPermitRejectsDirectCall(t *testing.T) {
	var zero outq.Permit
	called := false
	err := zero.Do(t.Context(), func(context.Context) error { called = true; return nil })
	if !errors.Is(err, outq.ErrNoPermit) || called {
		t.Fatalf("zero permit = %v called=%v, want ErrNoPermit and no call", err, called)
	}
	s, err := outq.New(&outq.Options{Pacer: newFakePacer(0)})
	if err != nil {
		t.Fatal(err)
	}
	if !s.Permit().Valid() {
		t.Fatal("minted permit is not valid")
	}
}

// TestPlatformAdapterContract is the GAP-GW-03 adapter boundary: a scheduler
// needs a pacer, a reply needs a send function, a long reply without an
// overflow sender is rejected, and an adapter cannot transmit without a
// scheduler permit.
func TestPlatformAdapterContract(t *testing.T) {
	if _, err := outq.New(nil); !errors.Is(err, outq.ErrNoPacer) {
		t.Fatalf("scheduler without pacer = %v, want ErrNoPacer", err)
	}
	s, err := outq.New(&outq.Options{Pacer: newFakePacer(0)})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Enqueue(t.Context(), &outq.Message{Platform: "telegram", Chat: "c", Parts: parts(1)}); !errors.Is(err, outq.ErrNoMessage) {
		t.Fatalf("reply without send = %v, want ErrNoMessage", err)
	}
	var zero outq.Permit
	if err := zero.Do(t.Context(), func(context.Context) error { return nil }); !errors.Is(err, outq.ErrNoPermit) {
		t.Fatalf("permit-less send = %v, want ErrNoPermit", err)
	}
}
