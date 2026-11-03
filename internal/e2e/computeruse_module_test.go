package e2e

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// writeCuaFixture writes the external cua backend the scenarios drive. It
// appends every argv to $CUA_LOG and answers screenshot with a 5.008-byte PNG
// (magic + filler), so the module's byte cap can be exercised without a real
// screen.
func writeCuaFixture(t *testing.T) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	bin, log = filepath.Join(dir, "cua"), filepath.Join(dir, "cua.log")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$CUA_LOG"
if [ "$1" = "screenshot" ]; then
  printf '\211PNG\r\n\032\n'
  i=0
  while [ "$i" -lt 500 ]; do
    printf '0123456789'
    i=$((i + 1))
  done
  exit 0
fi
printf 'ok'
`
	// #nosec G306 -- test fixture must be executable
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatalf("write cua fixture: %v", err)
	}
	return bin, log
}

// writeComputerUseConfig writes the [modules.computer_use] section used by the
// scenarios. The log path travels through the child environment allowlist.
func writeComputerUseConfig(t *testing.T, e *Env, enabled bool, bin string, capBytes int) {
	t.Helper()
	body := "[modules.computer_use]\nenabled = false\n"
	if enabled {
		body = "[modules.computer_use]\nenabled = true\ncommand = [\"" + bin + "\"]\nenv = [\"CUA_LOG\"]\nmax_screenshot_bytes = " + strconv.Itoa(capBytes) + "\n"
	}
	if err := os.WriteFile(e.cfg, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// cuaRunArgs is `run` with the module approval channel in the way: --approval
// auto keeps the core policy quiet so the module's own allow-once prompt is
// the only gate.
func cuaRunArgs(e *Env, prompt string) []string {
	return []string{
		"run", "-p", prompt,
		"--model", modelRef, "--base-url", e.Server.URL, "--api-key", "e2e",
		"--data-dir", e.DataDir, "--config", e.cfg,
		"--approval", "auto", "--output", "json",
	}
}

// cuaLogLines returns the arguments the fake backend received (0 lines when
// it was never started).
func cuaLogLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- fixture log under t.TempDir()
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read cua log: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// TestF6ComputerUseE2E is the W5 proof on the real binary: activation starts
// the external backend, a denied click runs zero actions, an approved click
// reaches it, a bounded screenshot lands tainted as
// <untrusted source="computer_use:process">, and the CLI start/stop lifecycle
// asks once before opening or closing it.
func TestF6ComputerUseE2E(t *testing.T) {
	t.Parallel()
	bin, log := writeCuaFixture(t)
	e := NewTrusted(t,
		ToolCallReply("s1", "tool_search", `{"query":"computer_use"}`),
		ToolCallReply("c1", "tool_invoke", `{"name":"computer_use","arguments":{"kind":"click","x":5,"y":6}}`),
		providertest.Text("done"),
	)
	writeComputerUseConfig(t, e, true, bin, 8192)
	res := e.ExecEnv([]string{"CUA_LOG=" + log}, cuaRunArgs(e, "click the fixture")...)
	res.RequireOK(t)
	if lines := cuaLogLines(t, log); len(lines) != 0 {
		t.Fatalf("denied click executed: %v", lines)
	}
	reqs := e.Requests()
	last := string(reqs[len(reqs)-1])
	if !strings.Contains(last, `\"code\":\"denied\"`) {
		t.Fatalf("denial missing from transcript:\n%.1200s", last)
	}
	// The CLI lifecycle asks once before touching the backend.
	if start := e.Exec("cua", "start", "--config", e.cfg); start.Code != 4 ||
		!strings.Contains(start.Stderr, "computer_use start backend=process") {
		t.Fatalf("unapproved start = %+v", start)
	}
	if start := e.Exec("cua", "start", "--config", e.cfg, "--yes"); start.Code != 0 || !strings.Contains(start.Stdout, "started") {
		t.Fatalf("approved start = %+v", start)
	}
	if stop := e.Exec("cua", "stop", "--config", e.cfg, "--yes"); stop.Code != 0 || !strings.Contains(stop.Stdout, "stopped") {
		t.Fatalf("approved stop = %+v", stop)
	}
}

// TestF6ComputerUseApprovedE2E: an approved click reaches the backend and the
// screenshot is bounded and tainted with the backend name.
func TestF6ComputerUseApprovedE2E(t *testing.T) {
	t.Parallel()
	bin, log := writeCuaFixture(t)
	e := NewTrusted(t,
		ToolCallReply("s1", "tool_search", `{"query":"computer_use"}`),
		ToolCallReply("c1", "tool_invoke", `{"name":"computer_use","arguments":{"kind":"click","x":5,"y":6}}`),
		ToolCallReply("p1", "tool_invoke", `{"name":"computer_use","arguments":{"kind":"screenshot"}}`),
		providertest.Text("done"),
	)
	writeComputerUseConfig(t, e, true, bin, 8192)
	res := e.ExecStdinEnv("y\n", []string{"CUA_LOG=" + log}, cuaRunArgs(e, "click and capture")...)
	res.RequireOK(t)
	lines := cuaLogLines(t, log)
	if len(lines) != 2 || lines[0] != "click --x 5 --y 6 --button left" || lines[1] != "screenshot" {
		t.Fatalf("backend argv = %v", lines)
	}
	reqs := e.Requests()
	last := string(reqs[len(reqs)-1])
	for _, want := range []string{`untrusted source=\"computer_use:process\"`, "screenshot 5008 bytes"} {
		if !strings.Contains(last, want) {
			t.Errorf("transcript lacks %q", want)
			logTranscript(t, reqs[len(reqs)-1])
		}
	}
}

// TestF6ComputerUseScreenshotCapE2E: a screenshot above max_screenshot_bytes
// fails too_large and never enters the transcript as an untrusted blob.
func TestF6ComputerUseScreenshotCapE2E(t *testing.T) {
	t.Parallel()
	bin, log := writeCuaFixture(t)
	e := NewTrusted(t,
		ToolCallReply("s1", "tool_search", `{"query":"computer_use"}`),
		ToolCallReply("p1", "tool_invoke", `{"name":"computer_use","arguments":{"kind":"screenshot"}}`),
		providertest.Text("done"),
	)
	writeComputerUseConfig(t, e, true, bin, 4096)
	res := e.ExecEnv([]string{"CUA_LOG=" + log}, cuaRunArgs(e, "capture the screen")...)
	res.RequireOK(t)
	reqs := e.Requests()
	last := string(reqs[len(reqs)-1])
	if !strings.Contains(last, "too_large") {
		t.Fatalf("cap denial missing:\n%.1200s", last)
	}
	if strings.Contains(last, `untrusted source=\"computer_use:`) {
		t.Fatalf("oversized screenshot entered the transcript:\n%.1200s", last)
	}
}

// TestF6ComputerUseDisabledE2E: disabled keeps computer_use out of the
// catalog, and a direct call is an unknown tool.
func TestF6ComputerUseDisabledE2E(t *testing.T) {
	t.Parallel()
	bin, _ := writeCuaFixture(t)
	e := New(t,
		ToolCallReply("c1", "computer_use", `{"kind":"screenshot"}`),
		providertest.Text("done"),
	)
	writeComputerUseConfig(t, e, false, bin, 0)
	res := e.Exec(cuaRunArgs(e, "capture the screen")...)
	res.RequireOK(t)
	reqs := e.Requests()
	if len(reqs) < 2 {
		t.Fatalf("provider requests = %d", len(reqs))
	}
	declared := webToolNames(t, reqs[0])
	for _, name := range []string{"tool_search", "tool_invoke", "computer_use"} {
		if declared[name] {
			t.Errorf("%s advertised with computer_use disabled: %v", name, declared)
		}
	}
	if !strings.Contains(string(reqs[1]), "unknown_tool") {
		t.Errorf("direct call was not refused:\n%.1200s", reqs[1])
	}
}
