// Package periodic is the internal periodic-task scheduler of `serve`
// (AUTO-029). It is driven by the single serveCron ticker: Tick decides which
// tasks are due, claims a slot, and runs it in the background so a slow task
// never blocks the ticker or another task. Per-task state (next slot, pause,
// crash window) lives in module_state through Store; that is what makes a
// restart skip an already consumed slot instead of running it twice.
//
// Semantics (recorded as decisions because SPEC only names the feature):
//   - Interval is wall-clock; Next advances from the previous slot, not from
//     now, so a late tick does not accumulate drift. A long downtime runs the
//     task once and re-aligns, never once per missed slot.
//   - FirstRun delays the first slot of a task from the first observation.
//   - Jitter is a stable per-name offset inside [-Jitter, +Jitter]; it spreads
//     task starts without changing the interval.
//   - A run is at most once per slot: the state is saved before Run starts.
//   - Pause/Resume and the crash window are persisted, so a restart neither
//     duplicates a consumed slot nor starts a task that was running.
//   - Budget bounds one run (context deadline); Preflight may skip a run.
//     Delivery happens at most once per terminal slot (ok, failed, budget;
//     skipped is not delivered) via a stable idempotency key. A failure is never
//     retried by this package.
package periodic

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Run outcomes.
const (
	OutcomeOK      = "ok"
	OutcomeFailed  = "failed"
	OutcomeSkipped = "skipped"
	OutcomeBudget  = "budget"
)

// Clock is the scheduler's wall clock.
type Clock interface {
	Now() time.Time
}

// State is the persisted row of one task. All times are wall clock; a zero
// field means "not set".
type State struct {
	Next         time.Time `json:"next,omitzero"`
	Last         time.Time `json:"last,omitzero"`
	RunningUntil time.Time `json:"running_until,omitzero"`
	Paused       bool      `json:"paused,omitempty"`
}

// Task is one internal periodic task. Name is the persisted key; Interval and
// Jitter are fixed at registration.
type Task struct {
	Name     string
	Interval time.Duration
	FirstRun time.Duration
	Jitter   time.Duration
	Budget   time.Duration
	// Preflight may skip the run (for example while the system is busy); it
	// runs inside the budget.
	Preflight func(ctx context.Context) (bool, error)
	Run       func(ctx context.Context) (string, error)
	// Deliver sends the output of a successful run to the Deliverer.
	Deliver bool
}

// Report is the terminal record of one slot, handed to Report (logging,
// incidents) after the state is saved.
type Report struct {
	Task        string
	ScheduledAt time.Time
	Outcome     string
	Err         string
	Output      string
	Duration    time.Duration
}

// Notice is one delivered result; IdemKey is stable per task slot.
type Notice struct {
	Task        string
	ScheduledAt time.Time
	Outcome     string
	Text        string
	IdemKey     string
}

// Deliverer is the outbox seam; the composition root implements it.
type Deliverer interface {
	Deliver(ctx context.Context, n *Notice) error
}

// Store persists one State per task name (module_state in production).
type Store interface {
	Load(ctx context.Context, name string) (State, bool, error)
	Save(ctx context.Context, name string, st *State) error
}

// Scheduler registers and evaluates periodic tasks. Tick is called by the one
// serve ticker; Wait drains in-flight runs on shutdown.
type Scheduler struct {
	Store   Store
	Clock   Clock
	Deliver Deliverer
	Report  func(*Report)

	mu      sync.Mutex
	tasks   []Task
	running map[string]bool
	wg      sync.WaitGroup
}

// Register adds a task; names are unique and limits are validated up front.
func (s *Scheduler) Register(t Task) error {
	switch {
	case t.Name == "":
		return errors.New("periodic: task name is empty")
	case t.Interval <= 0:
		return fmt.Errorf("periodic: task %q interval %s must be positive", t.Name, t.Interval)
	case t.FirstRun < 0:
		return fmt.Errorf("periodic: task %q first run %s must not be negative", t.Name, t.FirstRun)
	case t.Jitter < 0 || t.Jitter >= t.Interval:
		return fmt.Errorf("periodic: task %q jitter %s must be in [0, interval)", t.Name, t.Jitter)
	case t.Budget < 0:
		return fmt.Errorf("periodic: task %q budget %s must not be negative", t.Name, t.Budget)
	case t.Run == nil:
		return fmt.Errorf("periodic: task %q has no run function", t.Name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.tasks {
		if x.Name == t.Name {
			return fmt.Errorf("periodic: task %q is already registered", t.Name)
		}
	}
	s.tasks = append(s.tasks, t)
	return nil
}

// Tick claims and starts every due task. It returns state errors only; a
// task's own failure is asynchronously reported through Report.
func (s *Scheduler) Tick(ctx context.Context) error {
	if s.Store == nil {
		return nil
	}
	now := s.clock().Now()
	s.mu.Lock()
	tasks := append([]Task(nil), s.tasks...)
	s.mu.Unlock()
	var errs []error
	for i := range tasks {
		if err := s.tick(ctx, &tasks[i], now); err != nil {
			errs = append(errs, fmt.Errorf("periodic %s: %w", tasks[i].Name, err))
		}
	}
	return errors.Join(errs...)
}

// tick is the claim path: it holds mu across Load/Save so two ticks cannot
// claim the same task, and it saves the advanced slot before starting Run.
func (s *Scheduler) tick(ctx context.Context, t *Task, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running[t.Name] {
		return nil
	}
	if s.running == nil {
		s.running = map[string]bool{}
	}
	st, found, err := s.Store.Load(ctx, t.Name)
	if err != nil {
		return fmt.Errorf("load: %w", err)
	}
	if st.Paused || st.RunningUntil.After(now) {
		return nil
	}
	if !found || st.Next.IsZero() {
		st.Next = firstSlot(now, t)
		if st.Next.After(now) {
			return s.save(ctx, t.Name, &st)
		}
	}
	if now.Before(st.Next) {
		return nil
	}
	at := st.Next
	st.Last, st.Next = at, advance(at, now, t.Interval)
	st.RunningUntil = now.Add(runWindow(t))
	if err := s.save(ctx, t.Name, &st); err != nil {
		return err
	}
	s.running[t.Name] = true
	s.start(ctx, t, at, &st)
	return nil
}

// start runs the claimed slot in the background. The claim is already
// persisted, so a crash never repeats the slot; RunningUntil covers the window
// until the process is declared dead by the next tick after a restart.
func (s *Scheduler) start(ctx context.Context, t *Task, at time.Time, st *State) {
	s.wg.Go(func() {
		started := s.clock().Now()
		outcome, out, err := s.run(ctx, t)
		rep := Report{
			Task: t.Name, ScheduledAt: at, Outcome: outcome, Output: out,
			Duration: s.clock().Now().Sub(started),
		}
		if err != nil {
			rep.Err = err.Error()
		}
		saveCtx := context.WithoutCancel(ctx)
		if cur, found, lerr := s.Store.Load(saveCtx, t.Name); lerr != nil {
			rep.Err = joinErr(rep.Err, fmt.Sprintf("reload state: %s", lerr))
		} else if found {
			st = &cur
		}
		st.RunningUntil = time.Time{}
		if serr := s.Store.Save(saveCtx, t.Name, st); serr != nil {
			rep.Err = joinErr(rep.Err, fmt.Sprintf("save state: %s", serr))
		}
		s.mu.Lock()
		delete(s.running, t.Name)
		s.mu.Unlock()
		if t.Deliver && s.Deliver != nil && rep.Outcome != OutcomeSkipped {
			n := &Notice{
				Task: t.Name, ScheduledAt: at, Outcome: rep.Outcome, Text: out,
				IdemKey: fmt.Sprintf("periodic:%s:%d:1", t.Name, at.UnixMilli()),
			}
			if derr := s.Deliver.Deliver(saveCtx, n); derr != nil {
				rep.Err = joinErr(rep.Err, fmt.Sprintf("deliver: %s", derr))
			}
		}
		if s.Report != nil {
			s.Report(&rep)
		}
	})
}

// run applies the budget, then preflight, then the task body.
func (s *Scheduler) run(ctx context.Context, t *Task) (string, string, error) {
	runCtx := ctx
	if t.Budget > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, t.Budget)
		defer cancel()
	}
	if t.Preflight != nil {
		ok, err := t.Preflight(runCtx)
		if err != nil {
			return OutcomeFailed, "", err
		}
		if !ok {
			return OutcomeSkipped, "", nil
		}
	}
	out, err := t.Run(runCtx)
	switch {
	case err == nil:
		return OutcomeOK, out, nil
	case t.Budget > 0 && errors.Is(err, context.DeadlineExceeded):
		return OutcomeBudget, out, err
	default:
		return OutcomeFailed, out, err
	}
}

// Pause stops a task from starting; a running task finishes normally.
func (s *Scheduler) Pause(ctx context.Context, name string) error {
	st, err := s.load(ctx, name)
	if err != nil {
		return err
	}
	st.Paused = true
	if err := s.Store.Save(ctx, name, &st); err != nil {
		return fmt.Errorf("pause: %w", err)
	}
	return nil
}

// Resume schedules a fresh first run (FirstRun from now), not a burst of the
// slots that elapsed while paused.
func (s *Scheduler) Resume(ctx context.Context, name string) error {
	t, ok := s.task(name)
	if !ok {
		return fmt.Errorf("periodic: task %q is not registered", name)
	}
	st, err := s.load(ctx, name)
	if err != nil {
		return err
	}
	now := s.clock().Now()
	st.Paused = false
	st.Next = now.Add(t.FirstRun + jitter(name, t.Jitter))
	if st.Next.Before(now) {
		st.Next = now
	}
	if err := s.Store.Save(ctx, name, &st); err != nil {
		return fmt.Errorf("resume: %w", err)
	}
	return nil
}

// Wait blocks until in-flight runs finish or ctx expires.
func (s *Scheduler) Wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("periodic: wait: %w", ctx.Err())
	}
}

func (s *Scheduler) load(ctx context.Context, name string) (State, error) {
	if s.Store == nil {
		return State{}, errors.New("periodic: nil store")
	}
	st, _, err := s.Store.Load(ctx, name)
	if err != nil {
		return State{}, fmt.Errorf("load: %w", err)
	}
	return st, nil
}

func (s *Scheduler) task(name string) (Task, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.tasks {
		if t.Name == name {
			return t, true
		}
	}
	return Task{}, false
}

func (s *Scheduler) save(ctx context.Context, name string, st *State) error {
	if err := s.Store.Save(ctx, name, st); err != nil {
		return fmt.Errorf("save: %w", err)
	}
	return nil
}

func firstSlot(now time.Time, t *Task) time.Time {
	next := now.Add(t.FirstRun + jitter(t.Name, t.Jitter))
	if floor := now.Add(t.FirstRun); next.Before(floor) {
		return floor
	}
	return next
}

func (s *Scheduler) clock() Clock {
	if s.Clock == nil {
		return realClock{}
	}
	return s.Clock
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// advance moves a slot past now on the original cadence.
func advance(next, now time.Time, interval time.Duration) time.Time {
	steps := now.Sub(next)/interval + 1
	return next.Add(steps * interval)
}

// runWindow bounds a persisted claim: the run budget, or one interval when no
// budget is set.
func runWindow(t *Task) time.Duration {
	if t.Budget > 0 {
		return t.Budget
	}
	return t.Interval
}

// jitter is a stable offset from a task name inside [-j, +j].
func jitter(name string, j time.Duration) time.Duration {
	if j <= 0 {
		return 0
	}
	sum := sha256.Sum256([]byte(name))
	span := int64(2*j + 1)
	n := int64(binary.BigEndian.Uint32(sum[:4]))
	return time.Duration(n%span) - j
}

func joinErr(prev, next string) string {
	if prev == "" {
		return next
	}
	return prev + "; " + next
}
