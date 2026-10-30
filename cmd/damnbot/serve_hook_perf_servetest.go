//go:build servetest

package main

import (
	"context"
	"io"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	configos "github.com/damnbot/damnbot/internal/config"

	"github.com/damnbot/damnbot/internal/cron"
	"github.com/damnbot/damnbot/internal/exec"
	"github.com/damnbot/damnbot/internal/redact"
)

// The F4 budget harness (bench/f4, `make perf-f4`) needs the real serve
// process to carry the cron scheduler and, for the negative control, an
// artificial memory/CPU load. Production serve wires exactly one cron
// scheduler of its own (serveCronFor, 30 s tick), so the harness must not add
// a second ticker: it only runs one explicit catch-up Tick of an in-process
// script job when DAMNBOT_SERVE_TEST_CRON=1, which proves the cron path
// executes in the measured binary. Both seams live behind the `servetest` tag
// and never reach a release binary.
const perfCronJobID = "perf-f4-cron"

// registerServePerf installs the F4 harness seams and returns the stop func.
func registerServePerf(ctx context.Context, dataDir string, w io.Writer) func() {
	runCtx, cancel := context.WithCancel(ctx)
	stopCron := runPerfCronTick(runCtx, dataDir, w)
	stopLoad := startPerfLoad(runCtx, w)
	return func() {
		cancel()
		stopCron()
		stopLoad()
	}
}

// runPerfCronTick runs the real scheduler once over an in-process store with a
// one-minute script job (DAMNBOT_SERVE_TEST_CRON=1). The job touches a file in
// the data dir; a successful execution logs one line the harness counts. It
// deliberately starts no goroutine and no ticker: the only repeating cron
// scheduler in the process is the production serveCron.
func runPerfCronTick(ctx context.Context, dataDir string, w io.Writer) func() {
	if configos.OS().Getenv("DAMNBOT_SERVE_TEST_CRON") != "1" {
		return func() {}
	}
	runner := exec.New(ctx)
	sched := &cron.Scheduler{
		Store:    newPerfStore(dataDir),
		Executor: perfExecutor{inner: &cron.ScriptExecutor{Runner: runner, Redactor: redact.New(redact.Options{}), Timeout: 10 * time.Second}, w: w},
		Owner:    "perf-f4",
	}
	sessionsPrintf(w, "servetest: cron tick once (job=%s schedule=* * * * *)\n", perfCronJobID)
	if err := sched.Tick(ctx); err != nil {
		sessionsPrintf(w, "servetest: cron tick: %s\n", cleanLine(err.Error()))
	}
	return func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		if err := runner.CloseContext(closeCtx); err != nil {
			sessionsPrintf(w, "servetest: cron runner close: %s\n", cleanLine(err.Error()))
		}
	}
}

// perfExecutor logs each successful script run for the harness.
type perfExecutor struct {
	inner *cron.ScriptExecutor
	w     io.Writer
}

func (e perfExecutor) Run(ctx context.Context, j *cron.Job) (string, error) {
	out, err := e.inner.Run(ctx, j)
	if err == nil {
		sessionsPrintf(e.w, "servetest: cron run ok job=%s\n", j.ID)
	}
	return out, err
}

// startPerfLoad adds the negative-control load: DAMNBOT_SERVE_TEST_LOAD_MB
// megabytes of touched heap and DAMNBOT_SERVE_TEST_BURN spinning goroutines.
// The gate must fail when either knob is set.
func startPerfLoad(ctx context.Context, w io.Writer) func() {
	mb := envCount("DAMNBOT_SERVE_TEST_LOAD_MB")
	burn := envCount("DAMNBOT_SERVE_TEST_BURN")
	if mb == 0 && burn == 0 {
		return func() {}
	}
	sessionsPrintf(w, "servetest: artificial load %d MB, %d burn goroutines\n", mb, burn)
	done := make(chan struct{}, 2)
	if mb > 0 {
		go func() {
			defer func() { done <- struct{}{} }()
			buf := make([]byte, mb<<20)
			for i := 0; i < len(buf); i += 4096 {
				buf[i] = 1
			}
			<-ctx.Done()
			runtime.KeepAlive(buf)
		}()
	} else {
		done <- struct{}{}
	}
	for range burn {
		go func() {
			x := 0
			for {
				select {
				case <-ctx.Done():
					done <- struct{}{}
					return
				default:
					x = x*1664525 + 1013904223
				}
			}
		}()
	}
	return func() {
		for range 1 + max(burn, 1) {
			select {
			case <-done:
			case <-time.After(50 * time.Millisecond):
				return
			}
		}
	}
}

// envCount parses a non-negative integer knob; 0 when unset or invalid.
func envCount(name string) int {
	n, err := strconv.Atoi(configos.OS().Getenv(name))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// perfStore is the in-process scheduler store: one script job whose slots are
// claimed at most once, mirroring cron_runs UNIQUE(job_id, scheduled_at).
type perfStore struct {
	mu   sync.Mutex
	jobs []cron.Job
	rows map[int64]*cron.Run
}

func newPerfStore(dataDir string) *perfStore {
	return &perfStore{
		jobs: []cron.Job{{
			ID: perfCronJobID, Name: "perf-f4", Schedule: "* * * * *",
			Enabled: true, ApprovalMode: "auto", CatchUp: "run_once",
			MaxCatchUp: 1, Overlap: cron.OverlapSkip, Cwd: dataDir,
			CreatedAt: time.Now().Add(-2 * time.Minute),
			Script:    &cron.Script{Argv: []string{"/usr/bin/touch", filepath.Join(dataDir, "cron-perf-tick")}, Timeout: 10 * time.Second},
		}},
		rows: map[int64]*cron.Run{},
	}
}

func (s *perfStore) Jobs(context.Context) ([]cron.Job, error) { return s.jobs, nil }

func (s *perfStore) State(_ context.Context, jobID string) (cron.State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var st cron.State
	for at := range s.rows {
		if t := time.UnixMilli(at); t.After(st.Last) {
			st.Last = t
		}
	}
	return st, nil
}

func (s *perfStore) Active(context.Context, string) (cron.Active, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var a cron.Active
	for _, r := range s.rows {
		if r.StartedAt.IsZero() {
			a.Queued++
		} else if r.FinishedAt.IsZero() {
			a.Running++
		}
	}
	return a, nil
}

func (s *perfStore) Claim(_ context.Context, _ string, at, started time.Time, _ string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := at.UnixMilli()
	if _, ok := s.rows[key]; ok {
		return false, nil
	}
	s.rows[key] = &cron.Run{StartedAt: started}
	return true, nil
}

func (s *perfStore) TakeQueued(context.Context, string, time.Time) (time.Time, bool, error) {
	return time.Time{}, false, nil
}

func (s *perfStore) Record(_ context.Context, _ string, at time.Time, run *cron.Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[at.UnixMilli()] = run
	return nil
}

func (s *perfStore) Finish(_ context.Context, _ string, at time.Time, run *cron.Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[at.UnixMilli()] = run
	return nil
}

// compile-time assertions: the harness store satisfies the scheduler seams.
var (
	_ cron.Store    = (*perfStore)(nil)
	_ cron.Executor = perfExecutor{}
)
