package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/cron"
)

// CronJob is one scheduled job (TOOL-025, TSET-012).
type CronJob = cron.Job

const schemaCronJob = `{"name":"cronjob_manage","description":"Manage scheduled cron jobs: list, create, update, pause, resume, remove, run. Mutations need approval.","parameters":{"type":"object","properties":{"action":{"type":"string","enum":["list","create","update","pause","resume","remove","run"]},"job_id":{"type":"string","minLength":1,"maxLength":64},"name":{"type":"string","minLength":1,"maxLength":64},"schedule":{"type":"string","minLength":1,"maxLength":128},"prompt":{"type":"string","minLength":1,"maxLength":65536},"reason":{"type":"string","maxLength":500}},"required":["action"],"additionalProperties":false}}`

type cronJobTool struct{ admin *cron.Admin }

// NewCronJob returns the cronjob_manage tool (TOOL-025).
func NewCronJob(admin *cron.Admin) Tool { return &cronJobTool{admin: admin} }

func (t *cronJobTool) Name() string            { return "cronjob_manage" }
func (t *cronJobTool) Schema() json.RawMessage { return json.RawMessage(schemaCronJob) }
func (t *cronJobTool) Kind() Kind              { return KindEdit }

func (t *cronJobTool) Effect(args json.RawMessage) Effect {
	if cronAction(args) == "list" {
		return EffectRead
	}
	return EffectNonIdempotent
}

func (t *cronJobTool) Inspect(_ context.Context, _ Env, args json.RawMessage, _ PolicyContext) (PolicyDecision, error) {
	a := cronAction(args)
	if a == "list" {
		return PolicyDecision{Requirement: RequireAuto, Class: "read"}, nil
	}
	return PolicyDecision{Requirement: RequireAsk, Class: "cron:manage", AllowSession: false, Preview: "cronjob_manage: " + a}, nil
}

func cronAction(args json.RawMessage) string {
	var a struct {
		Action string `json:"action"`
	}
	if json.Unmarshal(args, &a) != nil {
		return ""
	}
	return a.Action
}

type cronArgs struct {
	Action   string `json:"action"`
	JobID    string `json:"job_id"`
	Name     string `json:"name"`
	Schedule string `json:"schedule"`
	Prompt   string `json:"prompt"`
	Reason   string `json:"reason"`
}

func (t *cronJobTool) Run(ctx context.Context, env Env, args json.RawMessage) (Result, error) {
	if t.admin == nil {
		return Result{}, toolErr(CodeModuleUnavailable, "cron management is not wired", "")
	}
	var a cronArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, &ToolError{Code: CodeInvalidArgs, Message: "arguments: " + err.Error(), Err: err}
	}
	switch a.Action {
	case "list":
		return t.list(ctx)
	case "create":
		if a.Schedule == "" || a.Prompt == "" {
			return Result{}, toolErr(CodeInvalidArgs, "create needs schedule and prompt", "")
		}
		if err := validSchedule(a.Schedule); err != nil {
			return Result{}, err
		}
		name := a.Name
		if name == "" {
			name = "job-" + strings.ReplaceAll(a.Schedule, " ", "-")
		}
		got, created, err := t.admin.Create(ctx, &cron.Job{Name: name, Schedule: a.Schedule, Prompt: a.Prompt, Cwd: env.Cwd, Enabled: true})
		if err != nil {
			if errors.Is(err, cron.ErrMinInterval) || errors.Is(err, cron.ErrJobQuota) {
				return Result{}, toolErr(CodeInvalidArgs, err.Error(), "raise the interval or remove an active job")
			}
			return Result{}, fmt.Errorf("cronjob_manage: create: %w", err)
		}
		verb := "created"
		if !created {
			verb = "already exists"
		}
		return Result{OK: true, Content: verb + ": " + got.ID}, nil
	case "update":
		return t.update(ctx, &a)
	case "pause", "resume", "remove", "run":
		return t.mutate(ctx, &a)
	}
	return Result{}, toolErr(CodeInvalidArgs, "action must be list, create, update, pause, resume, remove or run", "")
}

func (t *cronJobTool) mutate(ctx context.Context, a *cronArgs) (Result, error) {
	if a.JobID == "" {
		return Result{}, toolErr(CodeInvalidArgs, a.Action+" needs job_id", "call list first")
	}
	var err error
	switch a.Action {
	case "pause":
		err = t.admin.SetEnabled(ctx, a.JobID, false, a.Reason)
	case "resume":
		err = t.admin.SetEnabled(ctx, a.JobID, true, a.Reason)
	case "remove":
		err = t.admin.Remove(ctx, a.JobID)
	default:
		err = t.admin.Run(ctx, a.JobID)
	}
	if err != nil {
		return Result{}, fmt.Errorf("cronjob_manage: %s: %w", a.Action, err)
	}
	return Result{OK: true, Content: a.Action + "d: " + a.JobID}, nil
}

func (t *cronJobTool) list(ctx context.Context) (Result, error) {
	jobs, err := t.admin.List(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("cronjob_manage: list: %w", err)
	}
	var b strings.Builder
	for i := range jobs {
		j := &jobs[i]
		state := "paused"
		if j.Enabled {
			state = "active"
		}
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\n", j.ID, oneCronLine(j.Name), j.Schedule, state)
	}
	return Result{OK: true, Content: strings.TrimSuffix(b.String(), "\n")}, nil
}

func (t *cronJobTool) update(ctx context.Context, a *cronArgs) (Result, error) {
	if a.JobID == "" || (a.Name == "" && a.Schedule == "" && a.Prompt == "") {
		return Result{}, toolErr(CodeInvalidArgs, "update needs job_id and at least one field", "")
	}
	if a.Schedule != "" {
		if err := validSchedule(a.Schedule); err != nil {
			return Result{}, err
		}
	}
	jobs, err := t.admin.List(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("cronjob_manage: list: %w", err)
	}
	found := -1
	for i := range jobs {
		if jobs[i].ID == a.JobID {
			found = i
			break
		}
	}
	if found < 0 {
		return Result{}, toolErr(CodeNotFound, "cron job not found: "+a.JobID, "call action list")
	}
	job := &jobs[found]
	if a.Name != "" {
		job.Name = a.Name
	}
	if a.Schedule != "" {
		job.Schedule = a.Schedule
	}
	if a.Prompt != "" {
		job.Prompt = a.Prompt
	}
	if err := t.admin.Update(ctx, job); err != nil {
		if errors.Is(err, cron.ErrMinInterval) {
			return Result{}, toolErr(CodeInvalidArgs, err.Error(), "")
		}
		return Result{}, fmt.Errorf("cronjob_manage: update: %w", err)
	}
	return Result{OK: true, Content: "updated: " + a.JobID}, nil
}

func validSchedule(expr string) error {
	if _, err := cron.ParseSchedule(expr, time.Local); err != nil {
		return toolErr(CodeInvalidArgs, "invalid schedule: "+err.Error(), "")
	}
	return nil
}

func oneCronLine(s string) string { return strings.Join(strings.Fields(s), " ") }
