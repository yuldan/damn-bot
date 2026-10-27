package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/redact"
	"github.com/damnbot/damnbot/internal/tools"
)

type allowTools struct{ calls int }

func (a *allowTools) Approve(context.Context, *tools.ApprovalRequest) (tools.Approval, error) {
	a.calls++
	return tools.ApprovalAllowOnce, nil
}

type blockTools struct{ calls int }

func (b *blockTools) Approve(ctx context.Context, _ *tools.ApprovalRequest) (tools.Approval, error) {
	b.calls++
	<-ctx.Done()
	return tools.ApprovalDeny, fmt.Errorf("approval cancelled: %w", ctx.Err())
}

func bangFixture(t *testing.T, ws string) (*tuiRunner, string) {
	t.Helper()
	cfg := config.Default()
	cfg.Provider.Model = "openai/fake"
	cfg.Skills.TrustedProjects = []string{ws}
	o := &runOptions{
		prov: &injectProvider{}, newSessionID: "S-bang", sessionOrigin: "tui",
		approvalOrigin: "tui", approval: "ask",
	}
	r := newTUIRunner(&tuiDeps{
		cfg: &cfg, dataDir: t.TempDir(), modelRef: "openai/fake", opts: o,
		red: redact.New(redact.Options{}), configDir: t.TempDir(), home: t.TempDir(),
	})
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	model, err := r.ensure(t.Context())
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	return r, model.sessionID
}

// TestBangNoApproverDenies is the explicit negative control: the same shell
// command that succeeds with an approver never runs without one.
func TestBangNoApproverDenies(t *testing.T) {
	ws := realDir(t)
	t.Chdir(ws)
	r, sid := bangFixture(t, ws)
	target := filepath.Join(ws, "bangdir")
	res, err := r.Bang(t.Context(), sid, "mkdir bangdir", false)
	if err != nil {
		t.Fatalf("Bang: %v", err)
	}
	if res.OK || !res.Denied {
		t.Fatalf("no-approver bang = %+v", res)
	}
	if _, serr := os.Stat(target); !errors.Is(serr, os.ErrNotExist) {
		t.Fatalf("command ran without an approver: %v", serr)
	}
	t.Logf("denied output: %s", res.Output)
}

// TestBangApprovedRuns proves the positive path through the dispatcher.
func TestBangApprovedRuns(t *testing.T) {
	ws := realDir(t)
	t.Chdir(ws)
	r, sid := bangFixture(t, ws)
	appr := &allowTools{}
	r.deps.opts.toolApprover = appr
	res, err := r.Bang(t.Context(), sid, "mkdir bangdir", false)
	if err != nil {
		t.Fatalf("Bang: %v", err)
	}
	if !res.OK || appr.calls != 1 {
		t.Fatalf("approved bang = %+v calls=%d", res, appr.calls)
	}
	info, serr := os.Stat(filepath.Join(ws, "bangdir"))
	if serr != nil || !info.IsDir() {
		t.Fatalf("mkdir bang = %v, %v", info, serr)
	}
}

// TestBangPlanModeDenies: plan mode uses the same gate; an approver cannot
// widen it.
func TestBangPlanModeDenies(t *testing.T) {
	ws := realDir(t)
	t.Chdir(ws)
	r, sid := bangFixture(t, ws)
	appr := &allowTools{}
	r.deps.opts.toolApprover = appr
	res, err := r.Bang(t.Context(), sid, "mkdir plan.txt", true)
	if err != nil {
		t.Fatalf("Bang: %v", err)
	}
	if res.OK || appr.calls != 0 {
		t.Fatalf("plan-mode bang = %+v calls=%d", res, appr.calls)
	}
	if _, serr := os.Stat(filepath.Join(ws, "plan.txt")); !errors.Is(serr, os.ErrNotExist) {
		t.Fatalf("plan mode executed the command")
	}
}

// TestBangApprovalTimeoutDenies: a blocked approver times out and the command
// never runs.
func TestBangApprovalTimeoutDenies(t *testing.T) {
	ws := realDir(t)
	t.Chdir(ws)
	r, sid := bangFixture(t, ws)
	appr := &blockTools{}
	r.deps.opts.toolApprover = appr
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	res, err := r.Bang(ctx, sid, "mkdir slow.txt", false)
	if err != nil {
		t.Fatalf("Bang: %v", err)
	}
	if res.OK || appr.calls != 1 {
		t.Fatalf("timeout bang = %+v calls=%d", res, appr.calls)
	}
	if _, serr := os.Stat(filepath.Join(ws, "slow.txt")); !errors.Is(serr, os.ErrNotExist) {
		t.Fatalf("timed-out command executed")
	}
}
