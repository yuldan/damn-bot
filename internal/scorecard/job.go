package scorecard

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Defaults of the weekly job. SPEC §18.5 caps one scheduled eval run at
// US$5,00 (AUD-053) and alarms on a regression > 5%; the seed is fixed so two
// runs of the same week are byte-identical (GAP-INF-06).
const (
	DefaultSeed          int64   = 1
	DefaultDropThreshold float64 = 0.05
	DefaultMaxCostUSD    float64 = 5.00
)

// OwnerEnv is the gateway bootstrap variable (format platform:id) that names
// the owner chat. The package never reads the process environment itself
// (OPS-01); the composition root passes the value to OwnerFromEnv.
const OwnerEnv = "DAMNBOT_GATEWAY_OWNER"

// Closed errors. Compared with errors.Is.
var (
	// ErrRunnerUnavailable marks a run without a wired harness. The run is
	// stored as `skipped`; no score is invented.
	ErrRunnerUnavailable = errors.New("runner tidak tersedia")
	// ErrNoStore rejects Tick without an idempotency store.
	ErrNoStore = errors.New("store is required")
	// ErrNoOutbox rejects an alarm that must be sent but has no outbox seam.
	ErrNoOutbox = errors.New("outbox is required")
	// ErrNoRun is returned by Store.LastOK when no successful run exists.
	ErrNoRun = errors.New("no previous run")
)

// Clock is the job's time source (E-30: no bare sleep/ticker).
type Clock interface{ Now() time.Time }

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// Request is one weekly harness invocation.
type Request struct {
	Week       string
	WeekStart  time.Time
	Seed       int64
	MaxCostUSD float64
}

// Report is what the eval harness returned for one request.
type Report struct {
	Score      float64
	Dimensions []Dimension
	CostUSD    float64
}

// Runner runs the existing deterministic eval harness. bench/evals lives in a
// separate module, so the composition root supplies the adapter; a nil Runner
// is a valid runtime state and produces `skipped`.
type Runner interface {
	Run(ctx context.Context, req Request) (Report, error)
}

// Store is the per-week idempotency and artifact seam (scorecard_runs in
// production). Claim must insert at most one row per key; Finish stores the
// result; LastOK returns the newest successful result or ErrNoRun.
type Store interface {
	Claim(ctx context.Context, key string) (bool, error)
	Finish(ctx context.Context, key string, r *Result) error
	LastOK(ctx context.Context) (Result, error)
}

// Message is one outbound gateway message; it mirrors the fields the outbox
// needs from a caller (the adapter maps it to gateway.Message).
type Message struct {
	Platform string
	ChatID   string
	Key      string
	Body     string
}

// Outbox is the gateway outbox seam. The idempotency Key deduplicates a
// replayed alarm.
type Outbox interface {
	Enqueue(ctx context.Context, m Message) error
}

// Owner is the alarm chat (gateway role owner).
type Owner struct {
	Platform string
	ChatID   string
}

// OwnerFromEnv parses a DAMNBOT_GATEWAY_OWNER value ("platform:id", optionally
// comma-separated) and returns the first well-formed entry. An empty or
// malformed value means there is no alarm target.
func OwnerFromEnv(v string) (Owner, bool) {
	for item := range strings.SplitSeq(v, ",") {
		platform, chat, ok := strings.Cut(strings.TrimSpace(item), ":")
		if !ok || platform == "" || chat == "" {
			continue
		}
		if strings.ContainsAny(platform+chat, "\x00\r\n") {
			continue
		}
		return Owner{Platform: platform, ChatID: chat}, true
	}
	return Owner{}, false
}

// Job is the scorecard-weekly job. Zero Clock, Seed, DropThreshold, and
// MaxCostUSD take their documented defaults.
type Job struct {
	Clock         Clock
	Store         Store
	Runner        Runner
	Outbox        Outbox
	Owner         Owner
	Seed          int64
	DropThreshold float64
	MaxCostUSD    float64
}

// Tick evaluates the job at the clock's now. It returns the stored result and
// whether this call claimed a new week; a second tick in the same week returns
// (Result{}, false, nil) before touching the Runner. Runner failures are
// recorded (status failed) and returned with a nil error; store, finish, or
// alarm-enqueue failures are returned as errors after the result is stored.
func (j *Job) Tick(ctx context.Context) (Result, bool, error) {
	if j.Store == nil {
		return Result{}, false, fmt.Errorf("scorecard: %w", ErrNoStore)
	}
	now := j.clock().Now()
	week := WeekKey(now)
	prev, err := j.Store.LastOK(ctx)
	if err != nil && !errors.Is(err, ErrNoRun) {
		return Result{}, false, fmt.Errorf("scorecard: last run: %w", err)
	}
	claimed, err := j.Store.Claim(ctx, IdemKey(week))
	if err != nil {
		return Result{}, false, fmt.Errorf("scorecard: claim %s: %w", week, err)
	}
	if !claimed {
		return Result{}, false, nil
	}
	res := Result{
		Week: week, WeekStart: WeekStart(now), Seed: j.seed(), StartedAt: now,
		Dimensions: []Dimension{},
	}
	j.execute(ctx, &res)
	res.FinishedAt = j.clock().Now()
	if res.Status == StatusOK && prev.Status == StatusOK {
		res.Regressions = j.regressions(&prev, &res)
	}
	alarmErr := j.alarm(ctx, &res)
	if err := j.Store.Finish(ctx, IdemKey(week), &res); err != nil {
		return res, true, fmt.Errorf("scorecard: finish %s: %w", week, err)
	}
	if alarmErr != nil {
		return res, true, alarmErr
	}
	return res, true, nil
}

// execute fills res from the Runner; a nil Runner is skipped, never scored.
func (j *Job) execute(ctx context.Context, res *Result) {
	if j.Runner == nil {
		res.Status, res.Error = StatusSkipped, ErrRunnerUnavailable.Error()
		return
	}
	rep, err := j.Runner.Run(ctx, Request{
		Week:       res.Week,
		WeekStart:  res.WeekStart,
		Seed:       res.Seed,
		MaxCostUSD: j.maxCostUSD(),
	})
	if err != nil {
		res.Status, res.Error = StatusFailed, err.Error()
		return
	}
	res.Status, res.Score, res.CostUSD = StatusOK, rep.Score, rep.CostUSD
	res.Dimensions = normalize(rep.Dimensions)
}

// alarm enqueues exactly one message when the run failed or regressed past the
// threshold. No owner chat and no condition both yield an alarm status
// `skipped`; a failing outbox yields `failed` and an error.
func (j *Job) alarm(ctx context.Context, res *Result) error {
	if res.Status != StatusFailed && len(res.Regressions) == 0 {
		res.Alarm = Alarm{Status: StatusSkipped, Reason: "no alarm condition"}
		return nil
	}
	if j.Owner.Platform == "" || j.Owner.ChatID == "" {
		res.Alarm = Alarm{Status: StatusSkipped, Reason: "no owner chat"}
		return nil
	}
	if j.Outbox == nil {
		res.Alarm = Alarm{Status: StatusFailed, Reason: ErrNoOutbox.Error()}
		return fmt.Errorf("scorecard: %w", ErrNoOutbox)
	}
	err := j.Outbox.Enqueue(ctx, Message{
		Platform: j.Owner.Platform,
		ChatID:   j.Owner.ChatID,
		Key:      AlarmKey(res.Week),
		Body:     Summary(res),
	})
	if err != nil {
		res.Alarm = Alarm{Status: StatusFailed, Reason: err.Error()}
		return fmt.Errorf("scorecard: enqueue alarm %s: %w", res.Week, err)
	}
	res.Alarm = Alarm{Status: StatusOK, Reason: "enqueued"}
	return nil
}

// regressions lists the overall score and dimensions that dropped more than
// the threshold against the previous successful run.
func (j *Job) regressions(prev, cur *Result) []string {
	t := j.dropThreshold()
	var out []string
	if drop := relativeDrop(prev.Score, cur.Score); drop > t {
		out = append(out, fmt.Sprintf("score %.1f%%", drop*100))
	}
	old := make(map[string]float64, len(prev.Dimensions))
	for _, d := range prev.Dimensions {
		old[d.Name] = d.Score
	}
	for _, d := range cur.Dimensions {
		before, ok := old[d.Name]
		if !ok {
			continue
		}
		if drop := relativeDrop(before, d.Score); drop > t {
			out = append(out, fmt.Sprintf("dimension %s %.1f%%", d.Name, drop*100))
		}
	}
	return out
}

func (j *Job) clock() Clock {
	if j.Clock == nil {
		return realClock{}
	}
	return j.Clock
}

func (j *Job) seed() int64 {
	if j.Seed == 0 {
		return DefaultSeed
	}
	return j.Seed
}

func (j *Job) dropThreshold() float64 {
	if j.DropThreshold <= 0 {
		return DefaultDropThreshold
	}
	return j.DropThreshold
}

func (j *Job) maxCostUSD() float64 {
	if j.MaxCostUSD <= 0 {
		return DefaultMaxCostUSD
	}
	return j.MaxCostUSD
}
