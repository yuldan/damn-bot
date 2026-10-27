package tui

import (
	"errors"
	"strings"
	"testing"
)

func TestBangRendersToolBlock(t *testing.T) {
	t.Parallel()
	m, clk, _ := newTestApp(t)
	shell, ok := m.host.Shell.(*fakeShell)
	if !ok {
		t.Fatalf("shell = %T", m.host.Shell)
	}
	shell.res = BangResult{Output: "[exit 0 · 1ms]\nhello", OK: true, Duration: 2 * 1000 * 1000}
	m.input.SetValue("!echo hello")
	cmd := m.submit()
	if cmd == nil {
		t.Fatalf("!cmd returned no command")
	}
	if !m.bangBusy {
		t.Fatalf("bang did not mark itself busy")
	}
	m.Update(cmd())
	if shell.cmd != "echo hello" {
		t.Fatalf("bang dispatch = %q", shell.cmd)
	}
	if m.bangBusy {
		t.Fatalf("bang stayed busy")
	}
	last := m.blocks[len(m.blocks)-1]
	if last.Kind != BlockTool || last.ToolName != "shell" || last.Status != ToolOK ||
		last.ExitCode == nil || *last.ExitCode != 0 || last.Duration != 2*1000*1000 {
		t.Fatalf("bang block = %+v", last)
	}
	if last.At.Before(clk.Now()) {
		t.Fatalf("bang block has no clock stamp")
	}
}

func TestBangFailureAndPlan(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestApp(t)
	shell, ok := m.host.Shell.(*fakeShell)
	if !ok {
		t.Fatalf("shell = %T", m.host.Shell)
	}
	shell.res = BangResult{Output: "[exit 3 · 2ms]\nboom", OK: false, Denied: true}
	m.plan = true
	m.input.SetValue("!false")
	m.Update(m.submit()())
	if !shell.plan {
		t.Fatalf("plan mode was not forwarded")
	}
	last := m.blocks[len(m.blocks)-1]
	if last.Status != ToolFailed || last.ExitCode == nil || *last.ExitCode != 3 {
		t.Fatalf("failed bang block = %+v", last)
	}
}

func TestBangRefusals(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestApp(t)
	shell, ok := m.host.Shell.(*fakeShell)
	if !ok {
		t.Fatalf("shell = %T", m.host.Shell)
	}
	m.busy = true
	m.input.SetValue("!echo hi")
	m.submit()
	if shell.calls != 0 || !strings.Contains(lastBlockText(m), "bang refused") {
		t.Fatalf("busy bang ran: %q", lastBlockText(m))
	}
	m.busy = false
	shell.err = errors.New("denied by policy")
	m.input.SetValue("!echo hi")
	m.Update(m.submit()())
	if !strings.Contains(lastBlockText(m), "bang refused: denied by policy") {
		t.Fatalf("bang error hidden: %q", lastBlockText(m))
	}
}
