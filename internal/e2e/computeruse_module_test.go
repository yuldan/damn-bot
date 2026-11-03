package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// cuaFixtureSource is the external cua backend the scenarios drive. It appends
// every argv to $CUA_LOG and answers screenshot with a 5.008-byte PNG (magic +
// filler), so the module's byte cap can be exercised without a real screen.
const cuaFixtureSource = `package main

import (
	"os"
	"strings"
)

func main() {
	f, err := os.OpenFile(os.Getenv("CUA_LOG"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(1)
	}
	_, _ = f.WriteString(strings.Join(os.Args[1:], " ") + "\n")
	_ = f.Close()
	if len(os.Args) > 1 && os.Args[1] == "screenshot" {
		_, _ = os.Stdout.WriteString("\x89PNG\r\n\x1a\n" + strings.Repeat("0123456789", 500))
		return
	}
	_, _ = os.Stdout.WriteString("ok")
}
`

// writeCuaFixture builds cuaFixtureSource into a native binary (".exe" on
// Windows) so the process backend runs the same fixture on every CI OS.
func writeCuaFixture(t *testing.T) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "cua_fixture.go")
	if err := os.WriteFile(src, []byte(cuaFixtureSource), 0o600); err != nil {
		t.Fatalf("write cua fixture: %v", err)
	}
	name := "cua"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	bin, log = filepath.Join(dir, name), filepath.Join(dir, "cua.log")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", bin, src) // #nosec G204 -- fixed go tool
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build cua fixture: %v\n%s", err, out)
	}
	return bin, log
}

// writeComputerUseConfig writes the [modules.computer_use] section used by the
// scenarios. The log path travels through the child environment allowlist.
func writeComputerUseConfig(t *testing.T, e *Env, enabled bool, bin string, capBytes int) {
	t.Helper()
	body := "[modules.computer_use]\nenabled = false\n"
	if enabled {
		// strconv.Quote + ToSlash keeps Windows paths valid TOML basic strings.
		body = "[modules.computer_use]\nenabled = true\ncommand = [" + strconv.Quote(filepath.ToSlash(bin)) + "]\nenv = [\"CUA_LOG\"]\nmax_screenshot_bytes = " + strconv.Itoa(capBytes) + "\n"
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
