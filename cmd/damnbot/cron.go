package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/cron"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/exec"
	"github.com/damnbot/damnbot/internal/netx"
)

type cronOptions struct {
	configPath, dataDir, name, schedule, prompt string
	model, baseURL, apiKey, gate                string
	json                                        bool
}

// offered: cron_jobs has no script column yet (reported, no DDL here).
func runCron(args []string, stdout, stderr io.Writer) int {
	var o cronOptions
	fs := flag.NewFlagSet("cron", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.configPath, "config", "", "config.toml path")
	fs.StringVar(&o.dataDir, "data-dir", "", "data directory")
	fs.StringVar(&o.name, "name", "", "job name")
	fs.StringVar(&o.schedule, "schedule", "", "5-field cron expression")
	fs.StringVar(&o.prompt, "prompt", "", "self-contained prompt")
	fs.StringVar(&o.model, "model", "", "run: provider/model reference")
	fs.StringVar(&o.baseURL, "base-url", "", "run: internal/test provider base URL")
	fs.StringVar(&o.apiKey, "api-key", "", "run: internal/test provider API key")
	fs.StringVar(&o.gate, "gate", "", "add: gate JSON (script, wake or monitor)")
	fs.BoolVar(&o.json, "json", false, "machine-readable output")
	pos, err := parseInterleaved(fs, args)
	if err != nil || len(pos) == 0 {
		cronUsage(stderr)
		return 2
	}
	sub, rest := pos[0], pos[1:]
	switch sub {
	case "ls", "list", "status":
		if len(rest) != 0 {
			sessionsPrintf(stderr, "cron %s: unexpected argument %q\n", sub, cleanArg(rest[0]))
			return 2
		}
		return cronList(sub, &o, stdout, stderr)
	case "add":
		return cronAdd(rest, &o, stdout, stderr)
	case "pause", "resume", "rm", "run":
		return cronAct(sub, rest, &o, stdout, stderr)
	case "-h", "--help":
		cronUsage(stdout)
		return 0
	}
	sessionsPrintf(stderr, "cron: unknown subcommand %q\n", cleanArg(sub))
	cronUsage(stderr)
	return 2
}

func cronUsage(w io.Writer) {
	sessionsPrintf(w, "usage: damnbot cron <list|add|pause|resume|rm|run|status> [flags] [id]\n")
	sessionsPrintf(w, "  add --schedule S --prompt P   schedule a prompt job (--name)\n")
	sessionsPrintf(w, "  pause|resume|rm|run <id>      manage one job\n")
}

func cronList(sub string, o *cronOptions, stdout, stderr io.Writer) int {
	env, code := cronOpen(o, stderr)
	if env == nil {
		return code
	}
	defer func() {
		if err := env.close(); err != nil {
			sessionsPrintf(stderr, "cron: close: %s\n", cleanLine(err.Error()))
		}
	}()
	jobs, err := newCronStore(env.db).Jobs(context.Background())
	if err != nil {
		sessionsPrintf(stderr, "cron %s: %s\n", sub, cleanLine(err.Error()))
		return 1
	}
	if o.json {
		if sub == "status" {
			return cronStatusJSON(env.db, jobs, stdout, stderr)
		}
		return cronJSON(stdout, stderr, jobs)
	}
	if sub == "status" {
		active := 0
		for i := range jobs {
			if jobs[i].Enabled {
				active++
			}
		}
		gated, gerr := newCronStore(env.db).GatedRuns(context.Background())
		if gerr != nil {
			sessionsPrintf(stderr, "cron status: %s\n", cleanLine(gerr.Error()))
			return 1
		}
		return say(stdout, "cron: %d job(s), %d active, %d paused, %d gated\n", len(jobs), active, len(jobs)-active, gated)
	}
	for i := range jobs {
		j := &jobs[i]
		state, next := "paused", "-"
		if j.Enabled {
			state = "active"
			next = cronNext(j)
		}
		if code := say(stdout, "%s\t%s\t%s\t%s\t%s\n", j.ID, cleanLine(j.Name), j.Schedule, state, next); code != 0 {
			return code
		}
	}
	return 0
}

func cronStatusJSON(db *ctxdb.Store, jobs []cron.Job, stdout, stderr io.Writer) int {
	health, err := evaluateCronHealth(context.Background(), db, time.Now())
	if err != nil {
		return cronErr(stderr, "status", err)
	}
	return cronJSON(stdout, stderr, struct {
		Jobs   []cron.Job            `json:"jobs"`
		Health []cronHealthCondition `json:"health"`
	}{jobs, health})
}

func cronNext(j *cron.Job) string {
	sched, err := cron.ParseSchedule(j.Schedule, time.Local)
	if err != nil {
		return "-"
	}
	return sched.Next(time.Now()).Format(time.RFC3339)
}

func cronAdd(rest []string, o *cronOptions, stdout, stderr io.Writer) int {
	if len(rest) != 0 || o.schedule == "" || o.prompt == "" {
		sessionsPrintf(stderr, "cron add: --schedule and --prompt are required\n")
		return 2
	}
	if _, err := cron.ParseSchedule(o.schedule, time.Local); err != nil {
		sessionsPrintf(stderr, "cron add: %s\n", cleanLine(err.Error()))
		return 2
	}
	if _, err := cron.ParseGate(o.gate); err != nil {
		sessionsPrintf(stderr, "cron add: %s\n", cleanLine(err.Error()))
		return 2
	}
	cwd, cwdErr := os.Getwd()
	if cwdErr != nil {
		sessionsPrintf(stderr, "cron add: getwd: %s\n", cleanLine(cwdErr.Error()))
		return 1
	}
	if o.name == "" {
		o.name = fmt.Sprintf("job-%d", time.Now().UnixNano())
	}
	env, code := cronOpen(o, stderr)
	if env == nil {
		return code
	}
	defer func() {
		if err := env.close(); err != nil {
			sessionsPrintf(stderr, "cron: close: %s\n", cleanLine(err.Error()))
		}
	}()
	admin := &cron.Admin{
		Store: newCronStore(env.db), Owner: "local", Clock: wallClock{},
		Limits: cron.Limits{MinInterval: time.Minute, MaxJobs: 20},
	}
	job, created, err := admin.Create(context.Background(), &cron.Job{
		Name: o.name, Schedule: o.schedule, Prompt: o.prompt, Cwd: cwd, Gate: o.gate, Enabled: true,
	})
	if err != nil {
		sessionsPrintf(stderr, "cron add: %s\n", cleanLine(err.Error()))
		return 1
	}
	if o.json {
		return cronJSON(stdout, stderr, struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Created bool   `json:"created"`
		}{job.ID, job.Name, created})
	}
	verb := "created"
	if !created {
		verb = "already exists"
	}
	return say(stdout, "%s %s\t%s\n", verb, job.ID, cleanLine(job.Name))
}

func cronAct(sub string, rest []string, o *cronOptions, stdout, stderr io.Writer) int {
	if len(rest) != 1 {
		sessionsPrintf(stderr, "cron %s: exactly one job id is required\n", sub)
		return 2
	}
	id := rest[0]
	env, code := cronOpen(o, stderr)
	if env == nil {
		return code
	}
	defer func() {
		if err := env.close(); err != nil {
			sessionsPrintf(stderr, "cron: close: %s\n", cleanLine(err.Error()))
		}
	}()
	store := newCronStore(env.db)
	ctx := context.Background()
	switch sub {
	case "pause", "resume":
		if err := store.SetEnabled(ctx, id, sub == "resume", "cli "+sub); err != nil {
			sessionsPrintf(stderr, "cron %s: %s\n", sub, cleanLine(err.Error()))
			return 1
		}
		return say(stdout, "%sd %s\n", sub, id)
	case "rm":
		if err := store.Remove(ctx, id); err != nil {
			sessionsPrintf(stderr, "cron rm: %s\n", cleanLine(err.Error()))
			return 1
		}
		return say(stdout, "removed %s\n", id)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := exec.New(ctx)
	defer func() {
		if cerr := runner.Close(); cerr != nil {
			sessionsPrintf(stderr, "cron: close gate runner: %s\n", cleanLine(cerr.Error()))
		}
	}()
	admin := newCronAdmin(env.db, "cli", &cronPromptExecutor{
		configPath: o.configPath, dataDir: o.dataDir, model: o.model, baseURL: o.baseURL, apiKey: o.apiKey, stderr: stderr,
	}, wallClock{})
	admin.Limits.MinInterval = time.Minute
	netw := netx.New(netx.Config{Offline: env.cfg.Network.Offline, NoBackground: !env.cfg.Network.Background})
	admin.Gates = &cron.Gates{Runner: runner, Store: newCronStore(env.db), Fetch: monitorFetch(netw)}
	if err := admin.Run(ctx, id); err != nil {
		sessionsPrintf(stderr, "cron run: %s\n", cleanLine(err.Error()))
		return 1
	}
	return say(stdout, "ran %s\n", id)
}

type cronCLI struct {
	db  *ctxdb.Store
	cfg config.Config
}

func (c *cronCLI) close() error {
	if err := c.db.CloseContext(context.Background()); err != nil {
		return fmt.Errorf("close cron store: %w", err)
	}
	return nil
}

func cronOpen(o *cronOptions, stderr io.Writer) (*cronCLI, int) {
	env, err := openCLI(context.Background(), o.configPath, o.dataDir)
	if err != nil {
		sessionsPrintf(stderr, "cron: %s\n", cleanLine(err.Error()))
		return nil, 1
	}
	return &cronCLI{db: env.db, cfg: env.cfg}, 0
}

func cronJSON(w io.Writer, stderr io.Writer, v any) int {
	b, err := json.Marshal(v)
	if err != nil {
		return cronErr(stderr, "json", err)
	}
	return say(w, "%s\n", b)
}

func cronErr(stderr io.Writer, op string, err error) int {
	sessionsPrintf(stderr, "cron %s: %s\n", op, cleanLine(err.Error()))
	return 1
}
