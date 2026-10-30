package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/cron"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/gwstore"
	"github.com/damnbot/damnbot/internal/exec"
)

type cronRunInfo struct {
	JobID       string `json:"job_id"`
	ScheduledAt int64  `json:"scheduled_at"`
	StartedAt   int64  `json:"started_at,omitempty"`
	FinishedAt  int64  `json:"finished_at,omitempty"`
	Outcome     string `json:"outcome,omitempty"`
	StopReason  string `json:"stop_reason,omitempty"`
	Error       string `json:"error,omitempty"`
}

func cronRuns(ctx context.Context, db *ctxdb.Store, jobID string, limit int, incidents bool) ([]cronRunInfo, error) {
	if limit <= 0 {
		limit = 20
	}
	limit = min(limit, 200)
	where, args := "job_id = ?1", []any{jobID}
	if jobID == "" {
		where, args = "1 = 1", []any{}
	}
	if incidents {
		where += " AND (outcome IN ('failed','crashed') OR stop_reason LIKE 'preflight:%' OR stop_reason LIKE 'delivery:%')"
	}
	args = append(args, limit)
	out := make([]cronRunInfo, 0, limit)
	err := db.Write(ctx, func(tx *ctxdb.Tx) error {
		var err error
		out, err = queryCronRunInfo(tx, where, args)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("cron runs: %w", err)
	}
	return out, nil
}

func queryCronRunInfo(tx *ctxdb.Tx, where string, args []any) ([]cronRunInfo, error) {
	rows, err := tx.Strings(`SELECT job_id || char(31) || scheduled_at || char(31) || coalesce(started_at, 0) ||
		char(31) || coalesce(finished_at, 0) || char(31) || coalesce(outcome, '') || char(31) ||
		coalesce(stop_reason, '') || char(31) || coalesce(error, '')
		FROM cron_runs WHERE `+where+` ORDER BY scheduled_at DESC LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("cron runs: %w", err)
	}
	out := make([]cronRunInfo, 0, len(rows))
	for _, row := range rows {
		f := strings.Split(row, "\x1f")
		if len(f) != 7 {
			continue
		}
		out = append(out, cronRunInfo{
			JobID: f[0], ScheduledAt: atoi64(f[1]), StartedAt: atoi64(f[2]),
			FinishedAt: atoi64(f[3]), Outcome: f[4], StopReason: f[5], Error: f[6],
		})
	}
	return out, nil
}

func atoi64(s string) int64 {
	var n int64
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return 0
	}
	return n
}

func notepadKey(jobID string) string { return "notepad:" + jobID }

func notepadGet(ctx context.Context, db *ctxdb.Store, jobID string) (string, bool, error) {
	var raw string
	err := db.Write(ctx, func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT value FROM module_state WHERE module_id = 'cron' AND key = ?1`, []any{notepadKey(jobID)}, &raw)
	})
	if err != nil {
		if errors.Is(err, ctxdb.ErrNotFound) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("cron notepad: %w", err)
	}
	return raw, true, nil
}

func notepadSet(ctx context.Context, db *ctxdb.Store, jobID, text string) error {
	if len(text) > 4096 {
		return fmt.Errorf("cron notepad: text is %d bytes > 4096", len(text))
	}
	if err := db.Write(ctx, func(tx *ctxdb.Tx) error {
		_, err := tx.Exec(`INSERT INTO module_state(module_id, key, value, updated_at) VALUES ('cron', ?1, ?2, ?3)
			ON CONFLICT(module_id, key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			notepadKey(jobID), text, time.Now().UnixMilli())
		if err != nil {
			return fmt.Errorf("cron notepad: %w", err)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("cron notepad: %w", err)
	}
	return nil
}

func notepadClear(ctx context.Context, db *ctxdb.Store, jobID string) error {
	if err := db.Write(ctx, func(tx *ctxdb.Tx) error {
		_, err := tx.Exec(`DELETE FROM module_state WHERE module_id = 'cron' AND key = ?1`, notepadKey(jobID))
		if err != nil {
			return fmt.Errorf("cron notepad: %w", err)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("cron notepad: %w", err)
	}
	return nil
}

// cronDoctor reports read-only configuration findings.
func cronDoctor(ctx context.Context, db *ctxdb.Store) ([]string, error) {
	jobs, err := newCronStore(db).Jobs(ctx)
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	var findings []string
	for i := range jobs {
		j := &jobs[i]
		known[j.ID] = true
		if _, terr := cron.ParseSchedule(j.Schedule, time.Local); terr != nil {
			findings = append(findings, j.ID+": invalid schedule: "+terr.Error())
		}
		if j.TZ != "" {
			if _, terr := time.LoadLocation(j.TZ); terr != nil {
				findings = append(findings, j.ID+": unknown timezone "+j.TZ)
			}
		}
		if _, terr := cron.ParseGate(j.Gate); terr != nil {
			findings = append(findings, j.ID+": broken gate: "+terr.Error())
		}
	}
	orphans, err := orphanCronRuns(ctx, db, known)
	if err != nil {
		return nil, err
	}
	return append(findings, orphans...), nil
}

func orphanCronRuns(ctx context.Context, db *ctxdb.Store, known map[string]bool) ([]string, error) {
	var ids []string
	if err := db.Write(ctx, func(tx *ctxdb.Tx) error {
		var err error
		ids, err = tx.Strings(`SELECT DISTINCT job_id FROM cron_runs`)
		if err != nil {
			return fmt.Errorf("cron doctor: %w", err)
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("cron doctor: %w", err)
	}
	var out []string
	for _, id := range ids {
		if !known[id] {
			out = append(out, id+": orphan runs")
		}
	}
	return out, nil
}

func cronTick(ctx context.Context, o *cronOptions, stderr io.Writer) error {
	env, code := cronOpen(ctx, o, stderr)
	if env == nil {
		return fmt.Errorf("cron tick: open %d", code)
	}
	defer closeCronEnv(ctx, env, stderr)
	runner := exec.New(ctx)
	defer closeCronRunner(ctx, runner, stderr)
	out := &cronOutbox{store: gwstore.Store{DB: env.db}, offline: func() bool { return true }}
	admin := newCronAdmin(env.db, "cli", &cronPromptExecutor{
		configPath: o.configPath, dataDir: o.dataDir, model: o.model, baseURL: o.baseURL, apiKey: o.apiKey, stderr: stderr,
	}, wallClock{})
	admin.Gates = &cron.Gates{Runner: runner}
	admin.Limits.MinInterval = time.Minute
	err := (&cron.Scheduler{
		Store: admin.Store, Executor: &cronPromptExecutor{configPath: o.configPath, dataDir: o.dataDir, stderr: stderr},
		Incidents: &cronIncidents{db: env.db}, Alert: out, Clock: wallClock{}, Owner: "cli",
		Gates: &cron.Gates{Runner: runner},
	}).Tick(ctx)
	if err != nil {
		return fmt.Errorf("cron tick: %w", err)
	}
	return nil
}

func printRuns(w io.Writer, runs []cronRunInfo) {
	for _, r := range runs {
		line := fmt.Sprintf("%s\t%s\t%s", r.JobID, time.UnixMilli(r.ScheduledAt).UTC().Format(time.RFC3339), r.Outcome)
		if r.StopReason != "" {
			line += "\t" + r.StopReason
		}
		if r.Error != "" {
			line += "\t" + r.Error
		}
		sessionsPrintf(w, "%s\n", line)
	}
}

func printFindings(w io.Writer, findings []string) {
	for _, f := range findings {
		sessionsPrintf(w, "doctor: %s\n", f)
	}
}

func cronEdit(rest []string, o *cronOptions, stdout, stderr io.Writer) int {
	ctx := context.Background()
	if len(rest) != 1 {
		sessionsPrintf(stderr, "cron edit: exactly one job id is required\n")
		return 2
	}
	env, code := cronOpen(ctx, o, stderr)
	if env == nil {
		return code
	}
	defer closeCronEnv(ctx, env, stderr)
	jobs, err := newCronStore(env.db).Jobs(ctx)
	if err != nil {
		return cronErr(stderr, "edit", err)
	}
	found := -1
	for i := range jobs {
		if jobs[i].ID == rest[0] {
			found = i
			break
		}
	}
	if found < 0 {
		sessionsPrintf(stderr, "cron edit: job %s not found\n", cleanArg(rest[0]))
		return 1
	}
	job := &jobs[found]
	if o.name != "" {
		job.Name = o.name
	}
	if o.schedule != "" {
		if _, err := cron.ParseSchedule(o.schedule, time.Local); err != nil {
			sessionsPrintf(stderr, "cron edit: %s\n", cleanLine(err.Error()))
			return 2
		}
		job.Schedule = o.schedule
	}
	if o.prompt != "" {
		job.Prompt = o.prompt
	}
	if o.gate != "" {
		if _, err := cron.ParseGate(o.gate); err != nil {
			sessionsPrintf(stderr, "cron edit: %s\n", cleanLine(err.Error()))
			return 2
		}
		job.Gate = o.gate
	}
	admin := &cron.Admin{Store: newCronStore(env.db), Owner: "local", Clock: wallClock{}, Limits: cron.Limits{MinInterval: time.Minute, MaxJobs: 20}}
	if err := admin.Update(ctx, job); err != nil {
		return cronErr(stderr, "edit", err)
	}
	return say(stdout, "updated %s\n", job.ID)
}

func cronRunsCmd(sub string, rest []string, o *cronOptions, stdout, stderr io.Writer) int {
	ctx := context.Background()
	if len(rest) != 1 {
		sessionsPrintf(stderr, "cron %s: exactly one job id is required\n", sub)
		return 2
	}
	env, code := cronOpen(ctx, o, stderr)
	if env == nil {
		return code
	}
	defer closeCronEnv(ctx, env, stderr)
	runs, err := cronRuns(context.Background(), env.db, rest[0], o.limit, sub == "incidents")
	if err != nil {
		return cronErr(stderr, sub, err)
	}
	if o.json {
		return cronJSON(stdout, stderr, runs)
	}
	printRuns(stdout, runs)
	return 0
}

func cronNotepad(rest []string, o *cronOptions, stdout, stderr io.Writer) int {
	ctx := context.Background()
	if len(rest) == 0 {
		sessionsPrintf(stderr, "cron notepad: job id is required\n")
		return 2
	}
	env, code := cronOpen(ctx, o, stderr)
	if env == nil {
		return code
	}
	defer closeCronEnv(ctx, env, stderr)
	id := rest[0]
	if o.clear {
		if err := notepadClear(ctx, env.db, id); err != nil {
			return cronErr(stderr, "notepad", err)
		}
		return say(stdout, "notepad cleared %s\n", id)
	}
	if len(rest) == 1 {
		text, found, err := notepadGet(ctx, env.db, id)
		if err != nil {
			return cronErr(stderr, "notepad", err)
		}
		if !found {
			return say(stdout, "notepad %s: (empty)\n", id)
		}
		return say(stdout, "%s\n", text)
	}
	if err := notepadSet(ctx, env.db, id, strings.Join(rest[1:], " ")); err != nil {
		return cronErr(stderr, "notepad", err)
	}
	return say(stdout, "notepad set %s\n", id)
}

func cronDoctorCmd(o *cronOptions, stdout, stderr io.Writer) int {
	ctx := context.Background()
	env, code := cronOpen(ctx, o, stderr)
	if env == nil {
		return code
	}
	defer closeCronEnv(ctx, env, stderr)
	findings, err := cronDoctor(context.Background(), env.db)
	if err != nil {
		return cronErr(stderr, "doctor", err)
	}
	if o.json {
		return cronJSON(stdout, stderr, findings)
	}
	printFindings(stdout, findings)
	if len(findings) > 0 {
		return 1
	}
	return say(stdout, "cron doctor: ok\n")
}

func cronTickCmd(o *cronOptions, stdout, stderr io.Writer) int {
	if err := cronTick(context.Background(), o, stderr); err != nil {
		return cronErr(stderr, "tick", err)
	}
	return say(stdout, "tick: ok\n")
}

func closeCronEnv(ctx context.Context, env *cronCLI, stderr io.Writer) {
	if err := env.close(ctx); err != nil {
		sessionsPrintf(stderr, "cron: close: %s\n", cleanLine(err.Error()))
	}
}

func closeCronRunner(ctx context.Context, r *exec.Runner, stderr io.Writer) {
	if err := r.CloseContext(ctx); err != nil {
		sessionsPrintf(stderr, "cron: close runner: %s\n", cleanLine(err.Error()))
	}
}
