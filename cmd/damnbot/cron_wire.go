package main

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/damnbot/damnbot/internal/cron"
	"github.com/damnbot/damnbot/internal/ctxdb"
)

func newCronAdmin(db *ctxdb.Store, owner string, exec cron.Executor, clock cron.Clock) *cron.Admin {
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
