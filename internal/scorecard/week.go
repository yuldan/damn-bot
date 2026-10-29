package scorecard

import (
	"fmt"
	"time"
)

// JobID is the built-in cron job name (SPEC §18.5 `scorecard-weekly`).
const JobID = "scorecard-weekly"

// Schedule is the default 5-field cron expression in Zone: every Monday at
// 00:00. SPEC §18.5 fixes only "mingguan"; Monday 00:00 UTC is the weekly
// boundary used by WeekKey, so the cron slot and the idempotency key agree.
const Schedule = "0 0 * * 1"

// Zone is the time zone the schedule and the week key are evaluated in.
const Zone = "UTC"

// WeekStart returns Monday 00:00 UTC of the ISO-8601 week containing t.
func WeekStart(t time.Time) time.Time {
	u := t.UTC()
	day := time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
	return day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7))
}

// WeekKey returns the run key of the ISO week containing t ("2026-W44").
// Two ticks in the same week produce the same key; the next Monday produces a
// new one.
func WeekKey(t time.Time) string {
	year, week := WeekStart(t).ISOWeek()
	return fmt.Sprintf("%04d-W%02d", year, week)
}

// IdemKey is the per-job, per-week claim key. It is stable across restarts and
// processes, so Claim can admit at most one run per week.
func IdemKey(week string) string { return JobID + ":" + week }

// AlarmKey is the gateway-outbox idempotency key of the week's alarm. The
// outbox deduplicates it, so a replay never sends two alarms.
func AlarmKey(week string) string { return IdemKey(week) + ":alarm" }
