package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"time"

	"github.com/damnbot/damnbot/internal/cron"
	"github.com/damnbot/damnbot/internal/ctxdb"
)

func newCronAdmin(db *ctxdb.Store, owner string, exec cron.Executor, clock cron.Clock) *cron.Admin {
	if clock == nil {
		clock = wallClock{}
	}
	return &cron.Admin{Store: newCronStore(db), Exec: exec, Clock: clock, Owner: owner}
}

// cronPromptExecutor runs a prompt job as one headless turn with origin cron.
// Per-job max_cost_usd and approval_mode are not applied yet (reported).
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
		sessionOrigin: "cron", approvalOrigin: "run", approval: "ask",
	}, &out, e.stderr)
	if err != nil {
		return out.String(), err
	}
	if code != 0 {
		return out.String(), fmt.Errorf("cron: turn exit %d", code)
	}
	return out.String(), nil
}

// wallClock is the serve scheduler clock (time.After, never a ticker).
type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }
func (wallClock) After(d time.Duration) <-chan time.Time {
	if d > 0 {
		return time.After(d)
	}
	ch := make(chan time.Time, 1)
	ch <- time.Now()
	return ch
}

const (
	serveCronInterval = 30 * time.Second
	serveCronMaxDelay = 5 * time.Minute
)

// serveCron is AUTO-029: one catch-up Tick at start, then every interval; a
// failing tick backs off up to serveCronMaxDelay.
type serveCron struct {
	sched    *cron.Scheduler
	clock    cron.Clock
	interval time.Duration
	stderr   io.Writer
	cancel   context.CancelFunc
	done     chan struct{}
}

func newServeCron(store cron.Store, exec cron.Executor, clock cron.Clock, interval time.Duration, stderr io.Writer) *serveCron {
	if interval <= 0 {
		interval = serveCronInterval
	}
	return &serveCron{
		sched: &cron.Scheduler{Store: store, Executor: exec, Clock: clock, Owner: "serve"},
		clock: clock, interval: interval, stderr: stderr, done: make(chan struct{}),
	}
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
		if err := c.sched.Tick(ctx); err != nil {
			delay = min(max(delay*2, c.interval), serveCronMaxDelay)
			sessionsPrintf(c.stderr, "cron: tick: %s\n", cleanLine(err.Error()))
		} else {
			delay = c.interval
		}
	}
}

func startServeCron(ctx context.Context, db *ctxdb.Store, dataDir, configPath string, stderr io.Writer) *serveCron {
	exec := &cronPromptExecutor{configPath: configPath, dataDir: dataDir, stderr: stderr}
	c := newServeCron(newCronStore(db), exec, wallClock{}, serveCronInterval, stderr)
	c.Start(ctx)
	return c
}
