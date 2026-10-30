package cron

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Clock is the scheduler's time source (E-30: no bare sleep/ticker).
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Script is the argv of a script job (AUTO-007).
type Script struct {
	Argv      []string
	Timeout   time.Duration // 0: 600 s
	MaxOutput int           // 0: 64 KiB
}

// Overlap policies (REL-08, SPEC §11.5 cron_jobs.overlap).
const (
	OverlapSkip     = "skip"
	OverlapQueueOne = "queue_one"
	OverlapParallel = "parallel"
)

// Stored outcomes (SPEC §11.5 cron_runs.outcome).
const (
	OutcomeOK             = "ok"
	OutcomeFailed         = "failed"
	OutcomeMissed         = "missed"
	OutcomeSkippedOverlap = "skipped_overlap"
	OutcomeGated          = "gated" // preflight/gate prevented execution (COST-13)
)

// Job is a cron_jobs row plus the runtime-only Script (ADR-248).
type Job struct {
	ID, Name, Schedule, TZ string
	Prompt                 string
	Owner                  string
	Enabled                bool
	CatchUp, ApprovalMode  string
	MaxCatchUp             int
	Overlap                string
	Cwd                    string
	Gate                   string
	// GateContext is runtime-only gate context, never stored on the row.
	GateContext string
	CreatedAt   time.Time
	Script      *Script
}

// State is the newest recorded slot (catch-up watermark).
type State struct{ Last time.Time }

// Active is the open-slot census of one job: Running rows hold a claim with a
// start time, Queued rows are claimed but wait for the active run (REL-08).
type Active struct{ Running, Queued int }

// Run is the stored outcome and monitor record of a claimed slot. StartedAt
// and FinishedAt become cron_runs.started_at/finished_at (duration is their
// difference); Exit and Attempt go to stop_reason because the table has no
// dedicated columns (reported as a follow-up, no DDL in this PR).
type Run struct {
	Outcome, Err          string
	Exit, Attempt         int
	StartedAt, FinishedAt time.Time
	// Delivery and Incident are appended to cron_runs.stop_reason by the
	// store: "queued"/"skipped: …"/"failed: …" and "failed"/"preflight"/
	// "delivery" (AUTO-006/AUTO-008, no DDL).
	Delivery, Incident string
}

// JobStore lists job definitions and their catch-up watermark.
type JobStore interface {
	Jobs(ctx context.Context) ([]Job, error)
	State(ctx context.Context, jobID string) (State, error)
}

// RunStore persists slot claims; Claim inserts at most one row per
// (jobID, scheduledAt), so a restart never runs a slot twice.
type RunStore interface {
	Active(ctx context.Context, jobID string) (Active, error)
	// Claim inserts the slot; a zero started queues it instead of running it.
	Claim(ctx context.Context, jobID string, at, started time.Time, owner string) (bool, error)
	// TakeQueued starts the oldest queued slot and returns its scheduled time.
	TakeQueued(ctx context.Context, jobID string, started time.Time) (time.Time, bool, error)
	// Record writes a terminal row (missed/skipped_overlap) for an unclaimed slot.
	Record(ctx context.Context, jobID string, at time.Time, run *Run) error
	Finish(ctx context.Context, jobID string, at time.Time, run *Run) error
}

// Store is the full scheduler storage.
type Store interface {
	JobStore
	RunStore
}

// Delivery is one result for a platform; IdemKey is stable per slot (REL-10).
type Delivery struct {
	JobID                    string
	ScheduledAt              time.Time
	Outcome, Output, IdemKey string
}

// Deliverer is the gateway outbox seam (AUTO-006). Returning ErrOffline means
// the platform is unreachable by policy: the slot is recorded as skipped and
// nothing is queued, so the message is not delivered late after reconnect.
type Deliverer interface {
	Deliver(ctx context.Context, d *Delivery) error
}

// ErrOffline reports a delivery skipped because the gateway is offline.
var ErrOffline = errors.New("cron: delivery offline")

// Executor runs one job; ScriptExecutor is the script path.
type Executor interface {
	Run(ctx context.Context, j *Job) (string, error)
}

// ExecutorFunc adapts a function to Executor (the composition root's prompt
// runner and tests).
type ExecutorFunc func(ctx context.Context, j *Job) (string, error)

// Run implements Executor.
func (f ExecutorFunc) Run(ctx context.Context, j *Job) (string, error) { return f(ctx, j) }

// ExitStatus is implemented by executor errors that carry a process exit code.
type ExitStatus interface{ ExitCode() int }

// Scheduler evaluates due jobs; zero Grace means 60 s.
type Scheduler struct {
	Store     Store
	Executor  Executor
	Deliver   Deliverer
	Preflight Preflight
	Incidents IncidentStore
	Alert     Alerter
	Clock     Clock
	Owner     string
	Grace     time.Duration
	// Stopped is the emergency-stop gate (OPS-037). True claims nothing, leaving
	// due slots to catch-up on resume; an error fails closed.
	Stopped func(context.Context) (bool, error)
	// Gates evaluates Job.Gate before the executor (COST-13); nil fails closed.
	Gates GateRunner
}

// Tick evaluates every enabled job once at the clock's now.
func (s *Scheduler) Tick(ctx context.Context) error {
	if s.Store == nil {
		return errors.New("cron: nil store")
	}
	if stopped, err := s.stopped(ctx); err != nil || stopped {
		return err
	}
	jobs, err := s.Store.Jobs(ctx)
	if err != nil {
		return fmt.Errorf("cron: list jobs: %w", err)
	}
	now := s.clock().Now()
	var errs []error
	for i := range jobs {
		if !jobs[i].Enabled {
			continue
		}
		if stopped, serr := s.stopped(ctx); serr != nil || stopped {
			return serr
		}
		if err := s.tickJob(ctx, &jobs[i], now); err != nil {
			errs = append(errs, fmt.Errorf("cron: job %s: %w", jobs[i].ID, err))
		}
	}
	return errors.Join(errs...)
}

// RunOnce claims and executes one job now, outside its schedule (CLI
// `cron run`, cronjob_manage run and /cron run); a paused job still runs.
func (s *Scheduler) RunOnce(ctx context.Context, jobID string) error {
	jobs, err := s.Store.Jobs(ctx)
	if err != nil {
		return fmt.Errorf("cron: list jobs: %w", err)
	}
	for i := range jobs {
		if jobs[i].ID != jobID {
			continue
		}
		at := s.clock().Now()
		claimed, err := s.Store.Claim(ctx, jobID, at, at, s.Owner)
		if err != nil {
			return fmt.Errorf("cron: claim: %w", err)
		}
		if !claimed {
			return fmt.Errorf("cron: slot %s already claimed", at.Format(time.RFC3339Nano))
		}
		return s.execute(ctx, &jobs[i], at, at)
	}
	return fmt.Errorf("cron: job %q not found", jobID)
}

func (s *Scheduler) tickJob(ctx context.Context, j *Job, now time.Time) error {
	loc := time.Local
	if j.TZ != "" {
		l, err := time.LoadLocation(j.TZ)
		if err != nil {
			return fmt.Errorf("tz %q: %w", j.TZ, err)
		}
		loc = l
	}
	sched, err := ParseSchedule(j.Schedule, loc)
	if err != nil {
		return err
	}
	st, err := s.Store.State(ctx, j.ID)
	if err != nil {
		return fmt.Errorf("state: %w", err)
	}
	from := st.Last
	if from.IsZero() {
		from = j.CreatedAt
	}
	runs, missed := plan(j, sched.Slots(from, now), now, s.grace())
	for _, at := range missed {
		if err := s.Store.Record(ctx, j.ID, at, &Run{Outcome: OutcomeMissed}); err != nil {
			return fmt.Errorf("missed %s: %w", at.Format(time.RFC3339), err)
		}
	}
	if err := s.drain(ctx, j); err != nil {
		return err
	}
	for _, at := range runs {
		if err := s.slot(ctx, j, at); err != nil {
			return err
		}
	}
	return nil
}

// plan applies SELF-HEALING §6 catch-up; see the ADR/CHANGELOG for the table.
func plan(j *Job, slots []time.Time, now time.Time, grace time.Duration) (runs, missed []time.Time) {
	if len(slots) == 0 {
		return nil, nil
	}
	latest := slots[len(slots)-1]
	if now.Sub(latest) <= grace {
		return []time.Time{latest}, slots[:len(slots)-1]
	}
	switch j.CatchUp {
	case "skip":
		return nil, slots
	case "run_all":
		n := min(len(slots), max(j.MaxCatchUp, 1))
		return slots[len(slots)-n:], slots[:len(slots)-n]
	default: // run_once
		return []time.Time{latest}, slots[:len(slots)-1]
	}
}

// admission is the REL-08 decision for one due slot.
type admission int

const (
	admitStart admission = iota
	admitQueue
	admitDrop
)

// admit applies the overlap policy to the current open-slot census. Zero or
// unknown policies behave as the default "skip".
func admit(policy string, a Active) admission {
	if a.Running == 0 {
		return admitStart
	}
	switch policy {
	case OverlapQueueOne:
		if a.Queued == 0 {
			return admitQueue
		}
	case OverlapParallel:
		if a.Running < 2 {
			return admitStart
		}
	}
	return admitDrop
}

// slot admits, claims and (when admitted) executes one due slot.
func (s *Scheduler) slot(ctx context.Context, j *Job, at time.Time) error {
	a, err := s.Store.Active(ctx, j.ID)
	if err != nil {
		return fmt.Errorf("active: %w", err)
	}
	switch admit(j.Overlap, a) {
	case admitStart:
		started := s.clock().Now()
		claimed, cerr := s.Store.Claim(ctx, j.ID, at, started, s.Owner)
		if cerr != nil {
			return fmt.Errorf("claim: %w", cerr)
		}
		if !claimed {
			return nil
		}
		if xerr := s.execute(ctx, j, at, started); xerr != nil {
			return xerr
		}
		return s.drain(ctx, j)
	case admitQueue:
		_, cerr := s.Store.Claim(ctx, j.ID, at, time.Time{}, s.Owner)
		if cerr != nil {
			return fmt.Errorf("queue: %w", cerr)
		}
	case admitDrop:
		name := j.Overlap
		if name == "" {
			name = OverlapSkip
		}
		err = s.Store.Record(ctx, j.ID, at, &Run{
			Outcome: OutcomeSkippedOverlap,
			Err:     fmt.Sprintf("overlap %s: %d running, %d queued", name, a.Running, a.Queued),
		})
		if err != nil {
			return fmt.Errorf("record overlap: %w", err)
		}
	}
	return nil
}

// drain starts queued slots while nothing is running; queue_one keeps at most
// one waiting slot, so at most one run starts here.
func (s *Scheduler) drain(ctx context.Context, j *Job) error {
	for {
		a, err := s.Store.Active(ctx, j.ID)
		if err != nil {
			return fmt.Errorf("active: %w", err)
		}
		if a.Running > 0 || a.Queued == 0 {
			return nil
		}
		started := s.clock().Now()
		at, ok, err := s.Store.TakeQueued(ctx, j.ID, started)
		if err != nil {
			return fmt.Errorf("take queued: %w", err)
		}
		if !ok {
			return nil
		}
		if xerr := s.execute(ctx, j, at, started); xerr != nil {
			return xerr
		}
	}
}

// gate parses and evaluates Job.Gate. A nil result means "wake"; the returned
// GateResult is the skip verdict. A configured gate without a runner fails
// closed.
func (s *Scheduler) gate(ctx context.Context, j *Job) (*GateResult, error) {
	if strings.TrimSpace(j.Gate) == "" {
		return nil, nil
	}
	g, err := ParseGate(j.Gate)
	if err != nil {
		return nil, err
	}
	if s.Gates == nil {
		return nil, errors.New("cron: gate runner is not wired")
	}
	res, err := s.Gates.Check(ctx, j, g)
	if err != nil {
		return nil, err
	}
	if !res.Wake {
		return &res, nil
	}
	if res.Context != "" {
		j.GateContext = res.Context
	}
	return nil, nil
}

// execute runs one claimed slot and records its monitor fields. Preflight
// failure closes the slot as gated without touching the executor.
func (s *Scheduler) execute(ctx context.Context, j *Job, at, started time.Time) error {
	run := Run{Outcome: OutcomeOK, Attempt: 1, StartedAt: started}
	var (
		out string
		err error
	)
	if s.Preflight != nil {
		if perr := s.Preflight.Check(ctx, j); perr != nil {
			run.Outcome, run.Err = OutcomeGated, "preflight: "+perr.Error()
			run.FinishedAt = s.clock().Now()
			run.Incident = incidentKind(&run, nil)
			return errors.Join(s.finish(ctx, j, at, &run), s.report(ctx, j, &run, nil))
		}
	}
	if gate, gerr := s.gate(ctx, j); gerr != nil {
		run.Outcome, run.Err = OutcomeFailed, "gate: "+gerr.Error()
		run.FinishedAt = s.clock().Now()
		run.Incident = incidentKind(&run, nil)
		rerr := errors.Join(s.finish(ctx, j, at, &run), s.report(ctx, j, &run, nil))
		return errors.Join(gerr, rerr)
	} else if gate != nil {
		// A skipped gate is an expected quiet slot: it is recorded, never an
		// incident and never an LLM call.
		run.Outcome, run.Err = OutcomeGated, "gate: "+gate.Reason
		run.FinishedAt = s.clock().Now()
		return errors.Join(s.finish(ctx, j, at, &run), s.report(ctx, j, &run, nil))
	}
	if j.GateContext != "" && j.Script == nil {
		jc := *j
		jc.Prompt = strings.TrimSuffix(jc.Prompt, "\n") + "\n\ngate context: " + j.GateContext
		j = &jc
	}
	if s.Executor == nil {
		run.Outcome, run.Exit, run.Err = OutcomeFailed, 1, "cron: no executor"
	} else if out, err = s.Executor.Run(ctx, j); err != nil {
		run.Outcome, run.Exit, run.Err = OutcomeFailed, exitCode(err), err.Error()
	}
	run.FinishedAt = s.clock().Now()
	var derr error
	if s.Deliver != nil && run.Outcome == OutcomeOK {
		derr = s.Deliver.Deliver(ctx, &Delivery{
			JobID: j.ID, ScheduledAt: at, Outcome: run.Outcome, Output: out,
			IdemKey: fmt.Sprintf("cron:%s:%d:1", j.ID, at.UnixMilli()),
		})
		run.Delivery, derr = deliveryStatus(derr)
	}
	run.Incident = incidentKind(&run, derr)
	return errors.Join(s.finish(ctx, j, at, &run), s.report(ctx, j, &run, derr))
}

// deliveryStatus turns the deliverer result into the stop_reason marker. An
// offline gateway is an intentional skip, not a failing job: the slot is
// recorded as skipped with its reason and no incident is raised.
func deliveryStatus(err error) (status string, incident error) {
	switch {
	case err == nil:
		return "queued", nil
	case errors.Is(err, ErrOffline):
		return "skipped: " + err.Error(), nil
	default:
		return "failed: " + err.Error(), err
	}
}

// report persists the AUTO-008 incident streak and raises the hourly alarm.
func (s *Scheduler) report(ctx context.Context, j *Job, run *Run, derr error) error {
	if s.Incidents == nil {
		return nil
	}
	r := &Recorder{Store: s.Incidents, Alarm: s.Alert, Clock: s.Clock}
	msg := run.Err
	if derr != nil {
		msg = derr.Error()
	}
	if run.Incident == "" {
		return r.Success(ctx, j.ID)
	}
	return r.Failure(ctx, j.ID, j.Name, run.Incident, msg)
}

func (s *Scheduler) finish(ctx context.Context, j *Job, at time.Time, run *Run) error {
	if err := s.Store.Finish(ctx, j.ID, at, run); err != nil {
		return fmt.Errorf("finish: %w", err)
	}
	return nil
}

func exitCode(err error) int {
	var e ExitStatus
	if errors.As(err, &e) {
		return e.ExitCode()
	}
	return 1
}

func (s *Scheduler) grace() time.Duration {
	if s.Grace <= 0 {
		return 60 * time.Second
	}
	return s.Grace
}

func (s *Scheduler) stopped(ctx context.Context) (bool, error) {
	if s.Stopped == nil {
		return false, nil
	}
	stopped, err := s.Stopped(ctx)
	if err != nil {
		return true, fmt.Errorf("cron: emergency stop: %w", err)
	}
	return stopped, nil
}

func (s *Scheduler) clock() Clock {
	if s.Clock == nil {
		return realClock{}
	}
	return s.Clock
}
