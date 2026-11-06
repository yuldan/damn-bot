// Package outq is the central outbound scheduler of GAP-GW-03. Adapters hand
// it pre-rendered parts; it applies the per-chat, per-group and platform-global
// token buckets declared by Caps.Rate, enforces MaxChunksPerReply, records a
// Retry-After once (the adapter never sleeps inline), and schedules parts
// fairly across chats.
package outq

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Pacer is the token-bucket surface an adapter's Caps build. gateway.Limiter
// implements it; TakeChat must not block or sleep.
type Pacer interface {
	TakeChat(platform, chat string, group bool, at time.Time) (bool, time.Duration)
	Penalize(chat string, until time.Time)
}

// Priority orders ready work. Higher priorities are preferred; chats of equal
// priority are served round-robin.
type Priority uint8

// Priorities accepted by Enqueue. The zero value is PriorityNormal.
const (
	PriorityLow Priority = iota
	PriorityNormal
	PriorityHigh
)

// Part is one already-rendered chunk of a reply.
type Part struct {
	Text  string
	Index int
}

// Message is one reply. Parts are sent in order; above MaxChunks the tail is
// handed to Overflow as one markdown attachment (the adapter declares it).
type Message struct {
	Platform string
	Chat     string
	Group    bool
	Priority Priority
	Key      string
	Parts    []Part
	// Send transmits one part. It must honour ctx and must not sleep; pacing
	// and Retry-After are the scheduler's job.
	Send func(ctx context.Context, part Part) error
	// Overflow transmits the chunks beyond MaxChunksPerReply as one attachment.
	Overflow func(ctx context.Context, body string) error
	// RetryAfter extracts a platform retry_after from a send error. Nil means
	// no error carries one.
	RetryAfter func(error) (time.Duration, bool)
}

// Options configures a Scheduler. Zero values are production defaults.
type Options struct {
	Pacer             Pacer
	MaxChunksPerReply int
	Workers           int
	MaxAttempts       int
	RetryBase         time.Duration
	RetryMax          time.Duration
	OnError           func(Message, error)
	Now               func() time.Time
}

// Defaults of the outbound scheduler.
const (
	DefaultWorkers     = 4
	DefaultMaxAttempts = 4
	DefaultRetryBase   = time.Second
	DefaultRetryMax    = time.Minute
)

// Scheduler errors.
var (
	ErrNoPacer        = errors.New("outq: pacer is required")
	ErrNoMessage      = errors.New("outq: message is required")
	ErrNoSend         = errors.New("outq: a reply part has no send function")
	ErrOverflowNeeded = errors.New("outq: reply exceeds MaxChunksPerReply without an Overflow sender")
	ErrClosed         = errors.New("outq: scheduler is closed")
	ErrRunning        = errors.New("outq: scheduler already running")
	ErrNoPermit       = errors.New("outq: adapter has no scheduler permit")
)

// Scheduler sends replies under the adapter's declared buckets.
type Scheduler struct {
	pacer       Pacer
	maxChunks   int
	workers     int
	maxAttempts int
	retryBase   time.Duration
	retryMax    time.Duration
	onError     func(Message, error)
	now         func() time.Time

	mu       sync.Mutex
	queues   map[string][]*job
	order    []string
	rr       int
	inflight int
	closed   bool
	running  bool
	wake     chan struct{}
	stats    Stats
}

type job struct {
	msg      Message
	part     Part
	overflow string
	attempts int
	nextAt   time.Time
	priority Priority
}

// Stats is a point-in-time snapshot for /status and metrics.
type Stats struct {
	Queued    int
	InFlight  int
	Sent      int
	Retried   int
	Failed    int
	Throttled int
	MaxWait   time.Duration
}

// New builds a scheduler. New never starts a goroutine; Run does.
func New(opts *Options) (*Scheduler, error) {
	if opts == nil || opts.Pacer == nil {
		return nil, ErrNoPacer
	}
	s := &Scheduler{
		pacer:       opts.Pacer,
		maxChunks:   opts.MaxChunksPerReply,
		workers:     opts.Workers,
		maxAttempts: opts.MaxAttempts,
		retryBase:   opts.RetryBase,
		retryMax:    opts.RetryMax,
		onError:     opts.OnError,
		now:         opts.Now,
		queues:      make(map[string][]*job),
		wake:        make(chan struct{}, 1),
	}
	if s.maxChunks <= 0 {
		s.maxChunks = 5
	}
	if s.workers <= 0 {
		s.workers = DefaultWorkers
	}
	if s.maxAttempts <= 0 {
		s.maxAttempts = DefaultMaxAttempts
	}
	if s.retryBase <= 0 {
		s.retryBase = DefaultRetryBase
	}
	if s.retryMax <= 0 {
		s.retryMax = DefaultRetryMax
	}
	if s.now == nil {
		s.now = time.Now
	}
	return s, nil
}

// Permit is the scheduler-owned capability an adapter Send callback must
// present. A zero Permit is rejected, so a call path that bypasses outq cannot
// pass the contract test.
type Permit struct{ owner *Scheduler }

// Valid reports whether p was minted by a live Scheduler.
func (p Permit) Valid() bool { return p.owner != nil }

// Do runs fn under p; a missing permit is refused before fn executes.
func (p Permit) Do(ctx context.Context, fn func(context.Context) error) error {
	if !p.Valid() {
		return ErrNoPermit
	}
	if fn == nil {
		return fmt.Errorf("%w: nil action", ErrNoMessage)
	}
	return fn(ctx)
}

// Permit mints the adapter-facing permit of s.
func (s *Scheduler) Permit() Permit { return Permit{owner: s} }

// Enqueue splits msg into parts and appends them to its chat FIFO. The call
// never blocks on the network; it signals the dispatcher.
func (s *Scheduler) Enqueue(ctx context.Context, msg *Message) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("outq: enqueue: %w", err)
	}
	if msg == nil || strings.TrimSpace(msg.Chat) == "" || len(msg.Parts) == 0 || msg.Send == nil {
		return ErrNoMessage
	}
	jobs, err := s.split(msg)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if _, ok := s.queues[msg.Chat]; !ok {
		s.order = append(s.order, msg.Chat)
	}
	s.queues[msg.Chat] = append(s.queues[msg.Chat], jobs...)
	s.stats.Queued += len(jobs)
	s.signalLocked()
	return nil
}

func (s *Scheduler) split(msg *Message) ([]*job, error) {
	if msg.Priority > PriorityHigh {
		msg.Priority = PriorityNormal
	}
	visible := msg.Parts
	overflow := ""
	if len(visible) > s.maxChunks {
		if msg.Overflow == nil {
			return nil, ErrOverflowNeeded
		}
		body := make([]string, 0, len(visible)-s.maxChunks)
		for _, p := range visible[s.maxChunks:] {
			body = append(body, p.Text)
		}
		visible = visible[:s.maxChunks]
		overflow = strings.Join(body, "\n\n")
	}
	jobs := make([]*job, 0, len(visible)+1)
	for _, p := range visible {
		jobs = append(jobs, &job{msg: *msg, part: p, priority: msg.Priority})
	}
	if overflow != "" {
		jobs = append(jobs, &job{msg: *msg, overflow: overflow, priority: msg.Priority, part: Part{Index: len(visible)}})
	}
	return jobs, nil
}

// Run starts the worker pool and dispatcher and returns when ctx ends or
// Close is called. Enqueue may be called before or during Run.
func (s *Scheduler) Run(ctx context.Context) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return ErrRunning
	}
	s.running = true
	s.mu.Unlock()

	work := make(chan *job)
	done := make(chan result, s.workers)
	var wg sync.WaitGroup
	for range s.workers {
		wg.Go(func() { s.worker(ctx, work, done) })
	}
	s.loop(ctx, work, done)
	close(work)
	wg.Wait()
	return nil
}

// Close refuses new work and stops the dispatcher. Pending parts are dropped;
// the outbox rows remain the durable source of truth.
func (s *Scheduler) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.signalLocked()
	s.mu.Unlock()
}

// Stats returns the scheduler counters.
func (s *Scheduler) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.stats
	st.Queued = 0
	for _, q := range s.queues {
		st.Queued += len(q)
	}
	st.InFlight = s.inflight
	return st
}

func (s *Scheduler) signalLocked() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// backoff is the deterministic retry delay of attempt failures.
func (s *Scheduler) backoff(attempt int) time.Duration {
	delay := s.retryBase
	for i := 1; i < attempt && delay < s.retryMax; i++ {
		delay *= 2
	}
	return min(delay, s.retryMax)
}
