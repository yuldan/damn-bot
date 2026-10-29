package cron

import (
	"context"
	"errors"
	"fmt"
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

// Job is a cron_jobs row plus the runtime-only Script (ADR-248).
type Job struct {
	ID, Name, Schedule, TZ string
	Enabled                bool
	CatchUp, ApprovalMode  string
	MaxCatchUp             int
	Cwd                    string
	CreatedAt              time.Time
	Script                 *Script
}

// State is the newest recorded slot (catch-up watermark).
type State struct{ Last time.Time }

// Run is the stored outcome of a claimed slot (SPEC §11.5 enum).
type Run struct {
	Outcome, Err string
}

// Store persists jobs and slot claims; Claim inserts at most one row per
// (jobID, scheduledAt), so a restart never runs a slot twice.
type Store interface {
	Jobs(ctx context.Context) ([]Job, error)
	State(ctx context.Context, jobID string) (State, error)
	Claim(ctx context.Context, jobID string, at time.Time, owner string) (bool, error)
	Missed(ctx context.Context, jobID string, at time.Time) error
	Finish(ctx context.Context, jobID string, at time.Time, run Run) error
}

// Delivery is one result for a platform; IdemKey is stable per slot (REL-10).
type Delivery struct {
	JobID                    string
	ScheduledAt              time.Time
	Outcome, Output, IdemKey string
}

// Deliverer is the gateway outbox seam.
type Deliverer interface {
	Deliver(ctx context.Context, d *Delivery) error
}

// Executor runs one job; ScriptExecutor is the script path.
type Executor interface {
	Run(ctx context.Context, j *Job) (string, error)
}

// Scheduler evaluates due jobs; zero Grace means 60 s.
type Scheduler struct {
	Store    Store
	Executor Executor
	Deliver  Deliverer
	Clock    Clock
	Owner    string
	Grace    time.Duration
}

// Tick evaluates every enabled job once at the clock's now.
func (s *Scheduler) Tick(ctx context.Context) error {
	if s.Store == nil {
		return errors.New("cron: nil store")
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
		if err := s.tickJob(ctx, &jobs[i], now); err != nil {
			errs = append(errs, fmt.Errorf("cron: job %s: %w", jobs[i].ID, err))
		}
	}
	return errors.Join(errs...)
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
		if err := s.Store.Missed(ctx, j.ID, at); err != nil {
			return fmt.Errorf("missed %s: %w", at.Format(time.RFC3339), err)
		}
	}
	for _, at := range runs {
		if err := s.run(ctx, j, at); err != nil {
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

func (s *Scheduler) run(ctx context.Context, j *Job, at time.Time) error {
	ok, err := s.Store.Claim(ctx, j.ID, at, s.Owner)
	if err != nil {
		return fmt.Errorf("claim: %w", err)
	}
	if !ok {
		return nil
	}
	run, out := Run{Outcome: "ok"}, ""
	if s.Executor == nil {
		run = Run{Outcome: "failed", Err: "cron: no executor"}
	} else if out, err = s.Executor.Run(ctx, j); err != nil {
		run = Run{Outcome: "failed", Err: err.Error()}
	}
	if ferr := s.Store.Finish(ctx, j.ID, at, run); ferr != nil {
		return fmt.Errorf("finish: %w", ferr)
	}
	if s.Deliver == nil || run.Outcome != "ok" {
		return nil
	}
	err = s.Deliver.Deliver(ctx, &Delivery{
		JobID: j.ID, ScheduledAt: at, Outcome: run.Outcome, Output: out,
		IdemKey: fmt.Sprintf("cron:%s:%d:1", j.ID, at.UnixMilli()),
	})
	if err != nil {
		return fmt.Errorf("deliver: %w", err)
	}
	return nil
}

func (s *Scheduler) grace() time.Duration {
	if s.Grace <= 0 {
		return 60 * time.Second
	}
	return s.Grace
}

func (s *Scheduler) clock() Clock {
	if s.Clock == nil {
		return realClock{}
	}
	return s.Clock
}
