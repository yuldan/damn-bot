package cron

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Incident kinds persisted in module_state and mirrored in stop_reason.
const (
	IncidentFailed    = "failed"
	IncidentPreflight = "preflight"
	IncidentDelivery  = "delivery"
)

const (
	// AlarmAfter is the consecutive failure count that raises the first alarm.
	AlarmAfter = 3
	// AlarmEvery caps alarms per job (a success keeps the hourly clock).
	AlarmEvery = time.Hour
)

// Incident is the persisted streak of one job. Index-free: it lives in the
// existing module_state table (no migration).
type Incident struct {
	Failures  int       `json:"failures"`
	Kind      string    `json:"kind,omitempty"`
	Err       string    `json:"error,omitempty"`
	LastAlarm time.Time `json:"last_alarm,omitzero"`
}

// IncidentStore reads and writes the per-job incident row. The composition
// root implements it over module_state; cron stays storage-free.
type IncidentStore interface {
	Incident(ctx context.Context, jobID string) (Incident, error)
	SaveIncident(ctx context.Context, jobID string, inc Incident) error
}

// Alarm is one owner notification for a job that keeps failing.
type Alarm struct {
	JobID, JobName string
	Kind, Err      string
	Failures       int
	At             time.Time
}

// Alerter sends one alarm; the gateway outbox implementation is idempotent per
// (job, hour) key.
type Alerter interface {
	Alert(ctx context.Context, a *Alarm) error
}

// Recorder applies the AUTO-008 policy: count consecutive failures, alarm on
// the third and then at most once per AlarmEvery, reset the streak on success.
type Recorder struct {
	Store IncidentStore
	Alarm Alerter
	Clock Clock
}

// Success ends a failure streak. LastAlarm is intentionally kept: the hourly
// cap belongs to the job, not to one streak.
func (r *Recorder) Success(ctx context.Context, jobID string) error {
	if r == nil || r.Store == nil {
		return nil
	}
	inc, err := r.Store.Incident(ctx, jobID)
	if err != nil {
		return fmt.Errorf("cron incident %s: %w", jobID, err)
	}
	if inc.Failures == 0 && inc.Kind == "" && inc.Err == "" {
		return nil
	}
	inc.Failures, inc.Kind, inc.Err = 0, "", ""
	if err := r.Store.SaveIncident(ctx, jobID, inc); err != nil {
		return fmt.Errorf("cron incident %s: %w", jobID, err)
	}
	return nil
}

// Failure records one failed slot and alarms on the threshold/window.
func (r *Recorder) Failure(ctx context.Context, jobID, jobName, kind, msg string) error {
	if r == nil || r.Store == nil {
		return nil
	}
	now := r.clock().Now()
	inc, err := r.Store.Incident(ctx, jobID)
	if err != nil {
		return fmt.Errorf("cron incident %s: %w", jobID, err)
	}
	inc.Failures++
	inc.Kind, inc.Err = kind, truncate(msg, 400)
	var aerr error
	if r.Alarm != nil && inc.Failures >= AlarmAfter &&
		(inc.LastAlarm.IsZero() || now.Sub(inc.LastAlarm) >= AlarmEvery) {
		aerr = r.Alarm.Alert(ctx, &Alarm{
			JobID: jobID, JobName: jobName, Kind: kind, Err: inc.Err,
			Failures: inc.Failures, At: now,
		})
		switch {
		case aerr == nil:
			inc.LastAlarm = now
		case errors.Is(aerr, ErrOffline):
			// Nothing to send while the gateway is down; keep the hour unused
			// so the alarm leaves as soon as it is back, and do not fail Tick.
			aerr = nil
		}
	}
	serr := r.Store.SaveIncident(ctx, jobID, inc)
	var errs []error
	if aerr != nil {
		errs = append(errs, fmt.Errorf("cron alarm %s: %w", jobID, aerr))
	}
	if serr != nil {
		errs = append(errs, fmt.Errorf("cron incident %s: %w", jobID, serr))
	}
	return errors.Join(errs...)
}

func (r *Recorder) clock() Clock {
	if r.Clock == nil {
		return realClock{}
	}
	return r.Clock
}

// incidentKind classifies a finished run for stop_reason and the streak. An OK
// run with a delivery error is a delivery incident, never a failed job.
func incidentKind(run *Run, derr error) string {
	switch {
	case run.Outcome == OutcomeOK && derr == nil:
		return ""
	case run.Outcome == OutcomeGated:
		return IncidentPreflight
	case derr != nil:
		return IncidentDelivery
	default:
		return IncidentFailed
	}
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
