package gateway

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// ErrNoQuota, ErrNoTicket and ErrSchedulerClosed reject bad wiring and closed
// submission.
var (
	ErrNoQuota         = errors.New("gateway: quota is required")
	ErrNoTicket        = errors.New("gateway: ticket is required")
	ErrSchedulerClosed = errors.New("gateway: scheduler is closed")
)

// SchedulerOptions configures the turn scheduler. MaxParallelTurns is
// cfg.Gateway.MaxParallelTurns (SPEC §12, default 4, range 1..16).
type SchedulerOptions struct {
	Quota            *Quota
	MaxParallelTurns int
	// OnError observes a turn error; the scheduler keeps running.
	OnError func(Ticket, error)
}

// SubmitKind is the outcome of Submit; the zero value refuses.
type SubmitKind uint8

// Submit outcomes.
const (
	Refused SubmitKind = iota
	Enqueued
)

// RefusalKind says why Submit refused a message.
type RefusalKind uint8

// Refusal reasons.
const (
	RefusalNone      RefusalKind = iota
	RefusalQuota                 // a rate or daily quota was exhausted
	RefusalQueueFull             // the identity's FIFO is full
)

// Verdict is Submit's answer. Enqueued means the turn will run; Refused means
// no turn and no tool call will run. Message is the static reply the adapter
// may send, empty when the one-per-minute notice budget is spent.
type Verdict struct {
	Kind     SubmitKind
	Refusal  RefusalKind
	Quota    QuotaResult
	Position int
	Message  string
}

type turnJob struct {
	ticket Ticket
	work   RunFunc
}

type turnQueue struct {
	active bool
	jobs   []turnJob
}

// Scheduler runs admitted turns on a fixed pool of MaxParallelTurns workers
// (GAP-GW-02: a semaphore, never a goroutine per message). Each identity has
// at most one active turn and a FIFO of DefaultIdentityQueue waiting turns;
// the seventh message is refused with a readable reply, never dropped. Work
// runs under a scheduler-lifetime context, not the adapter request.
type Scheduler struct {
	quota   *Quota
	workers int
	onError func(Ticket, error)
	mu      sync.Mutex
	cond    *sync.Cond
	queues  map[string]*turnQueue
	order   []string
	started bool
	closed  bool
	wg      sync.WaitGroup
}

// NewScheduler builds a scheduler; no goroutine starts until the first Submit.
func NewScheduler(opts SchedulerOptions) (*Scheduler, error) {
	if opts.Quota == nil {
		return nil, ErrNoQuota
	}
	workers := opts.MaxParallelTurns
	if workers == 0 {
		workers = DefaultMaxParallelTurns
	}
	if workers < 1 || workers > 16 {
		return nil, fmt.Errorf("%w: max parallel turns %d is outside 1..16", ErrInvalidQuota, workers)
	}
	s := &Scheduler{
		quota:   opts.Quota,
		workers: workers,
		onError: opts.OnError,
		queues:  make(map[string]*turnQueue),
	}
	s.cond = sync.NewCond(&s.mu)
	return s, nil
}

// Submit applies the quota and enqueues work for one admitted update. It never
// blocks and never runs work on the caller's goroutine.
//
// The context of the first Submit starts the pool and becomes the lifetime of
// every turn (serve passes the daemon's run context, never one adapter
// request); later values are not consulted, and Close is the shutdown edge.
func (s *Scheduler) Submit(ctx context.Context, req QuotaRequest, t *Ticket, work RunFunc) (Verdict, error) {
	if t == nil {
		return Verdict{}, ErrNoTicket
	}
	if work == nil {
		return Verdict{}, ErrNoWork
	}
	if err := req.validate(); err != nil {
		return Verdict{}, err
	}
	// Everything is decided under one lock, so a message never consumes quota
	// only to be refused by a queue that filled in between. The only nested
	// lock is scheduler → quota; no quota path takes the scheduler lock.
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Verdict{}, ErrSchedulerClosed
	}
	key := req.Platform + "\x00" + req.Identity
	q := s.queues[key]
	if q != nil && len(q.jobs) >= DefaultIdentityQueue {
		return s.refuseQueue(req), nil
	}
	res, err := s.quota.Take(req)
	if err != nil {
		return Verdict{}, err
	}
	if res.Limit != LimitNone {
		v := Verdict{Refusal: RefusalQuota, Quota: res}
		if s.quota.Notice(req) {
			v.Message = res.Message()
		}
		return v, nil
	}
	if q == nil {
		q = &turnQueue{}
		s.queues[key] = q
		s.order = append(s.order, key)
	}
	ahead := len(q.jobs)
	if q.active {
		ahead++
	}
	q.jobs = append(q.jobs, turnJob{ticket: *t, work: work})
	s.startLocked(ctx)
	s.cond.Signal()
	return Verdict{Kind: Enqueued, Position: ahead + 1, Quota: res}, nil
}

func (s *Scheduler) refuseQueue(req QuotaRequest) Verdict {
	v := Verdict{Refusal: RefusalQueueFull}
	if s.quota.Notice(req) {
		v.Message = fmt.Sprintf("antre penuh (maks %d)", DefaultIdentityQueue)
	}
	return v
}

// Stats reports how many turns are waiting in the identity queues and how many
// are running. It is read-only and takes the same lock as Submit, so /status
// never observes a queue mid-update.
func (s *Scheduler) Stats() (queued, active int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, q := range s.queues {
		queued += len(q.jobs)
		if q.active {
			active++
		}
	}
	return queued, active
}

func (s *Scheduler) startLocked(ctx context.Context) {
	if s.started {
		return
	}
	s.started = true
	s.wg.Add(s.workers)
	for range s.workers {
		go s.worker(ctx)
	}
}

// Close stops the pool. A running turn finishes; queued turns are discarded
// (their updates remain in the durable inbound queue).
func (s *Scheduler) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.cond.Broadcast()
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *Scheduler) worker(ctx context.Context) {
	defer s.wg.Done()
	for {
		job, key, ok := s.acquire()
		if !ok {
			return
		}
		if err := job.work(ctx, job.ticket); err != nil && s.onError != nil {
			s.onError(job.ticket, err)
		}
		s.finish(key)
	}
}

// acquire marks an identity active before returning its job; at most one
// worker can do so, which bounds the identity to one running turn.
func (s *Scheduler) acquire() (turnJob, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		if s.closed {
			return turnJob{}, "", false
		}
		if job, key, ok := s.pickLocked(); ok {
			return job, key, true
		}
		s.cond.Wait()
	}
}

func (s *Scheduler) pickLocked() (turnJob, string, bool) {
	for i := 0; i < len(s.order); {
		key := s.order[i]
		q := s.queues[key]
		switch {
		case q == nil || (!q.active && len(q.jobs) == 0):
			s.order = append(s.order[:i], s.order[i+1:]...)
			delete(s.queues, key)
		case q.active:
			i++
		default:
			job := q.jobs[0]
			q.jobs = q.jobs[1:]
			q.active = true
			return job, key, true
		}
	}
	return turnJob{}, "", false
}

func (s *Scheduler) finish(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if q := s.queues[key]; q != nil {
		q.active = false
		if len(q.jobs) == 0 {
			for i, k := range s.order {
				if k == key {
					s.order = append(s.order[:i], s.order[i+1:]...)
					break
				}
			}
			delete(s.queues, key)
		}
	}
	s.cond.Signal()
}
