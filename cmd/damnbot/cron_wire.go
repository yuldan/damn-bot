package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/cron"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/cronstore"
	"github.com/damnbot/damnbot/internal/ctxdb/gwstore"
	"github.com/damnbot/damnbot/internal/gateway"
)

func newCronAdmin(db *ctxdb.Store, owner string, exec cron.Executor, clock cron.Clock) *cron.Admin {
	if clock == nil {
		clock = wallClock{}
	}
	return &cron.Admin{Store: newCronStore(db), Exec: exec, Clock: clock, Owner: owner}
}

// Production cron wiring (composition root): cron stays storage-free and
// cronstore is capped, so conversions, runner, outbox and incidents live here.

// cronAdapter converts cronstore rows to the cron value types.
type cronAdapter struct{ cronstore.Store }

func newCronAdapter(db *ctxdb.Store) *cronAdapter {
	return &cronAdapter{Store: cronstore.Store{DB: db}}
}

func (s *cronAdapter) Jobs(ctx context.Context) ([]cron.Job, error) {
	rows, err := s.Store.Jobs(ctx)
	if err != nil {
		return nil, fmt.Errorf("cron jobs: %w", err)
	}
	out := make([]cron.Job, len(rows))
	for i := range rows {
		r := &rows[i]
		out[i] = cron.Job{
			ID: r.ID, Name: r.Name, Schedule: r.Schedule, TZ: r.TZ, Prompt: r.Prompt,
			Enabled: r.Enabled, CatchUp: r.CatchUp, ApprovalMode: r.ApprovalMode, MaxCatchUp: r.MaxCatchUp,
			Overlap: r.Overlap, Cwd: r.Cwd, CreatedAt: r.CreatedAt,
		}
	}
	return out, nil
}

func (s *cronAdapter) State(ctx context.Context, id string) (cron.State, error) {
	st, err := s.Store.State(ctx, id)
	if err != nil {
		return cron.State{}, fmt.Errorf("cron state %s: %w", id, err)
	}
	return cron.State{Last: st.Last}, nil
}

func (s *cronAdapter) Active(ctx context.Context, id string) (cron.Active, error) {
	a, err := s.Store.Active(ctx, id)
	if err != nil {
		return cron.Active{}, fmt.Errorf("cron active %s: %w", id, err)
	}
	return cron.Active{Running: a.Running, Queued: a.Queued}, nil
}

func (s *cronAdapter) Claim(ctx context.Context, id string, at, started time.Time, owner string) (bool, error) {
	ok, err := s.Store.Claim(ctx, id, at, started, owner)
	if err != nil {
		return false, fmt.Errorf("cron claim %s: %w", id, err)
	}
	return ok, nil
}

func (s *cronAdapter) TakeQueued(ctx context.Context, id string, started time.Time) (time.Time, bool, error) {
	at, ok, err := s.Store.TakeQueued(ctx, id, started)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("cron queued %s: %w", id, err)
	}
	return at, ok, nil
}

// toStoreRun converts the scheduler's value type for the cron_runs writer.
func toStoreRun(run *cron.Run) *cronstore.Run {
	return &cronstore.Run{
		Outcome: run.Outcome, Err: run.Err, Exit: run.Exit, Attempt: run.Attempt,
		StartedAt: run.StartedAt, FinishedAt: run.FinishedAt,
		Delivery: run.Delivery, Incident: run.Incident,
	}
}

func (s *cronAdapter) Record(ctx context.Context, id string, at time.Time, run *cron.Run) error {
	if err := s.Store.Record(ctx, id, at, toStoreRun(run)); err != nil {
		return fmt.Errorf("cron record %s: %w", id, err)
	}
	return nil
}

func (s *cronAdapter) Finish(ctx context.Context, id string, at time.Time, run *cron.Run) error {
	if err := s.Store.Finish(ctx, id, at, toStoreRun(run)); err != nil {
		return fmt.Errorf("cron finish %s: %w", id, err)
	}
	return nil
}

// cronPromptExecutor runs a prompt job as one headless turn with origin cron.
type cronPromptExecutor struct {
	configPath, dataDir, model, baseURL, apiKey string
	stderr                                      io.Writer
}

func (e *cronPromptExecutor) Run(ctx context.Context, j *cron.Job) (string, error) {
	if j.Prompt == "" {
		return "", fmt.Errorf("cron: job %s has no prompt", j.ID)
	}
	var out bytes.Buffer
	code, err := runOne(ctx, &runOptions{
		prompt: j.Prompt, model: e.model, configPath: e.configPath,
		dataDir: e.dataDir, output: "text", baseURL: e.baseURL, apiKey: e.apiKey, cwd: j.Cwd,
		sessionOrigin: "cron", approvalOrigin: "run", approval: j.ApprovalMode,
	}, &out, e.stderr)
	if err != nil {
		return out.String(), err
	}
	if code != 0 {
		return out.String(), fmt.Errorf("cron: turn exit %d", code)
	}
	return out.String(), nil
}

// wallClock is the serve scheduler clock: time.After, never a ticker.
type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }
func (wallClock) After(d time.Duration) <-chan time.Time {
	if d <= 0 {
		ch := make(chan time.Time, 1)
		ch <- time.Now()
		return ch
	}
	return time.After(d)
}

// cronOutbox is the production cron.Deliverer/Alerter: one outbox row per stable key.
type cronOutbox struct {
	store   gwstore.Store
	offline func() bool
}

func (d *cronOutbox) Deliver(ctx context.Context, m *cron.Delivery) error {
	body := fmt.Sprintf("cron %s (%s) · %s", m.JobID, m.ScheduledAt.UTC().Format(time.RFC3339), m.Outcome)
	if out := strings.TrimSpace(m.Output); out != "" {
		body += "\n\n" + cronClip(out, 8<<10)
	}
	return d.enqueue(ctx, m.IdemKey, body)
}

func (d *cronOutbox) Alert(ctx context.Context, a *cron.Alarm) error {
	name := a.JobName
	if name == "" {
		name = a.JobID
	}
	body := fmt.Sprintf("cron %q failed %d× (%s): %s", name, a.Failures, a.Kind, cronClip(a.Err, 500))
	key := fmt.Sprintf("cron:alarm:%s:%d", a.JobID, a.At.UTC().Truncate(cron.AlarmEvery).Unix())
	return d.enqueue(ctx, key, body)
}

func (d *cronOutbox) enqueue(ctx context.Context, key, body string) error {
	if d.offline() {
		return fmt.Errorf("%w: gateway disabled or network.offline", cron.ErrOffline)
	}
	platform, chat, err := cronHomeChannel()
	if err != nil {
		return err
	}
	if _, _, err := d.store.EnqueueDelivery(ctx, &gwstore.Delivery{
		IdemKey:  gateway.DeliveryKey(platform, chat, key),
		Platform: platform, ChatID: chat, Body: body, Status: gateway.StatusQueued,
	}); err != nil {
		return fmt.Errorf("cron: enqueue: %w", err)
	}
	return nil
}

// cronHomeChannel is the home chat until gwstore.HomeChannel (#481) lands.
func cronHomeChannel() (string, string, error) {
	owner := strings.TrimSpace(os.Getenv("DAMNBOT_GATEWAY_OWNER"))
	platform, chat, ok := strings.Cut(owner, ":")
	if !ok || !gwstore.ValidIdentity(platform) || !gwstore.ValidIdentity(chat) {
		return "", "", fmt.Errorf("%w: DAMNBOT_GATEWAY_OWNER=platform:id is required", cron.ErrOffline)
	}
	return platform, chat, nil
}

func cronClip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// cronJobOf returns the job id of a cron result outbox key, or "".
func cronJobOf(idem string) string {
	i := strings.LastIndexByte(idem, '\x1f')
	if i < 0 {
		return ""
	}
	rest, ok := strings.CutPrefix(idem[i+1:], "cron:")
	if !ok || strings.HasPrefix(rest, "alarm:") {
		return ""
	}
	job, _, ok := strings.Cut(rest, ":")
	if !ok {
		return ""
	}
	return job
}

// deadCronDelivery records an outbox row that died after all retries.
func (g *serveGateway) deadCronDelivery(ctx context.Context, idem, cause string) {
	if g == nil || g.cron == nil {
		return
	}
	job := cronJobOf(idem)
	if job == "" {
		return
	}
	if err := g.cron.Failure(ctx, job, job, cron.IncidentDelivery, "delivery failed: "+cause); err != nil {
		sessionsPrintf(g.stderr, "cron: incident: %s\n", cleanLine(err.Error()))
	}
}

// cronIncidentModule keeps the AUTO-008 streak in one module_state JSON row/job.
const cronIncidentModule = "cron"

type cronIncidents struct{ db *ctxdb.Store }

func (s *cronIncidents) Incident(ctx context.Context, jobID string) (cron.Incident, error) {
	var raw string
	err := s.db.Write(ctx, func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT value FROM module_state WHERE module_id = ?1 AND key = ?2`,
			[]any{cronIncidentModule, "incident:" + jobID}, &raw)
	})
	if err != nil {
		if errors.Is(err, ctxdb.ErrNotFound) {
			return cron.Incident{}, nil
		}
		return cron.Incident{}, fmt.Errorf("cron incident read %s: %w", jobID, err)
	}
	var inc cron.Incident
	if err := json.Unmarshal([]byte(raw), &inc); err != nil {
		return cron.Incident{}, fmt.Errorf("cron incident decode %s: %w", jobID, err)
	}
	return inc, nil
}

func (s *cronIncidents) SaveIncident(ctx context.Context, jobID string, inc cron.Incident) error {
	raw, err := json.Marshal(inc)
	if err != nil {
		return fmt.Errorf("cron incident encode %s: %w", jobID, err)
	}
	err = s.db.Write(ctx, func(tx *ctxdb.Tx) error {
		if _, xerr := tx.Exec(`INSERT INTO module_state(module_id, key, value, updated_at)
			VALUES (?1, ?2, ?3, ?4)
			ON CONFLICT(module_id, key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			cronIncidentModule, "incident:"+jobID, string(raw), time.Now().UnixMilli()); xerr != nil {
			return fmt.Errorf("write module_state: %w", xerr)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("cron incident write %s: %w", jobID, err)
	}
	return nil
}

// Serve ticker: one immediate catch-up Tick, then every interval (backoff ≤ 5 min).
const (
	serveCronInterval = 30 * time.Second
	serveCronMaxDelay = 5 * time.Minute
)

type serveCron struct {
	sched    *cron.Scheduler
	db       *ctxdb.Store
	clock    cron.Clock
	interval time.Duration
	stderr   io.Writer
	cancel   context.CancelFunc
	done     chan struct{}
}

// newServeCron assembles store → executor → deliverer → incidents.
func newServeCron(db *ctxdb.Store, dataDir, configPath string, deliver bool, stderr io.Writer) *serveCron {
	out := &cronOutbox{
		store:   gwstore.Store{DB: db},
		offline: func() bool { return !deliver },
	}
	incidents := &cronIncidents{db: db}
	clock := wallClock{}
	return &serveCron{
		sched: &cron.Scheduler{
			Store: newCronAdapter(db), Executor: &cronPromptExecutor{configPath: configPath, dataDir: dataDir, stderr: stderr},
			Deliver: out, Preflight: &cron.Checks{DataDir: dataDir},
			Incidents: incidents, Alert: out, Clock: clock, Owner: "serve",
		},
		db: db, clock: clock, interval: serveCronInterval, stderr: stderr, done: make(chan struct{}),
	}
}

// serveCronFor builds the ticker and shares its recorder with the gateway outbox.
func serveCronFor(gw *serveGateway, db *ctxdb.Store, dataDir, configPath string, offline bool, stderr io.Writer) *serveCron {
	cr := newServeCron(db, dataDir, configPath, gw != nil && !offline, stderr)
	if gw != nil {
		gw.cron = cr.recorder()
	}
	return cr
}

// recorder shares the incident policy with the gateway outbox.
func (c *serveCron) recorder() *cron.Recorder {
	return &cron.Recorder{Store: c.sched.Incidents, Alarm: c.sched.Alert, Clock: c.clock}
}
func (c *serveCron) Start(ctx context.Context) {
	runCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	go c.loop(runCtx)
}

func (c *serveCron) Stop() {
	if c.cancel != nil {
		c.cancel()
	}
	<-c.done
}

func (c *serveCron) loop(ctx context.Context) {
	defer close(c.done)
	delay := time.Duration(0)
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.clock.After(delay):
		}
		if err := c.tick(ctx); err != nil {
			delay = min(max(delay*2, c.interval), serveCronMaxDelay)
			sessionsPrintf(c.stderr, "cron: tick: %s\n", cleanLine(err.Error()))
		} else {
			delay = c.interval
		}
	}
}

// tick skips the scheduler write transaction while no job is enabled.
func (c *serveCron) tick(ctx context.Context) error {
	n, err := c.db.Count(ctx, `SELECT EXISTS(SELECT 1 FROM cron_jobs WHERE enabled = 1)`)
	if err != nil {
		return fmt.Errorf("cron jobs probe: %w", err)
	}
	if n == 0 {
		return nil
	}
	if err := c.sched.Tick(ctx); err != nil {
		return fmt.Errorf("cron tick: %w", err)
	}
	return nil
}
