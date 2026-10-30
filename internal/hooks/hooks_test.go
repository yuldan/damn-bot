package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestMain doubles as the hook program: tests copy the test binary into the
// hooks directory under a mode name, so the child needs no shell and runs
// identically on every CI OS.
func TestMain(m *testing.M) {
	switch mode := strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe"); mode {
	case "allow", "deny", "exit", "timeout", "truncate", "env", "redact", "garbage":
		hookProgram(mode)
		return
	}
	m.Run()
}

func hookProgram(mode string) {
	body, err := io.ReadAll(os.Stdin)
	if err != nil {
		syscall.Exit(5)
	}
	switch mode {
	case "allow":
		if !allowInput(body) {
			syscall.Exit(4)
		}
		writeOut(`{"decision":"allow"}`)
	case "deny":
		writeOut(`{"decision":"deny","reason":"blocked by test"}`)
	case "exit":
		writeErr("boom")
		syscall.Exit(3)
	case "timeout":
		<-time.NewTimer(time.Hour).C
	case "truncate":
		writeOut(`{"decision":"allow"}` + strings.Repeat(" ", 200))
	case "env":
		reason := "marker=" + os.Getenv("HOOK_MARKER_SECRET") +
			",leak=" + os.Getenv("DAMNBOT_TEST_LEAK") +
			",path=" + map[bool]string{true: "yes", false: "no"}[os.Getenv("PATH") != ""]
		out, merr := json.Marshal(map[string]string{"decision": "allow", "reason": reason})
		if merr != nil {
			syscall.Exit(5)
		}
		writeBytes(out)
	case "redact":
		writeOut(`{"decision":"allow","reason":"redact-me-123456"}`)
	case "garbage":
		writeOut("not json")
	}
}

func allowInput(body []byte) bool {
	var in map[string]any
	return os.Args[1] == string(EventGatewayInbound) &&
		json.Unmarshal(body, &in) == nil &&
		in["version"] == Version && in["event"] == string(EventGatewayInbound) &&
		in["platform"] == "telegram" && in["chat_id"] == "1" &&
		in["user_id"] == "2" && in["text"] == "hi"
}

func writeOut(s string) {
	if _, err := os.Stdout.WriteString(s); err != nil {
		syscall.Exit(5)
	}
}

func writeErr(s string) {
	if _, err := os.Stderr.WriteString(s); err != nil {
		syscall.Exit(5)
	}
}

func writeBytes(b []byte) {
	if _, err := os.Stdout.Write(b); err != nil {
		syscall.Exit(5)
	}
}

func TestRunAllow(t *testing.T) {
	t.Parallel()
	reg, dir := testRegistry(t)
	hookFile(t, dir, "allow")
	res, err := reg.Run(context.Background(), "allow", EventGatewayInbound, map[string]any{
		"platform": "telegram", "chat_id": "1", "user_id": "2", "text": "hi",
	})
	if err != nil {
		t.Fatalf("Run: %v; stderr=%q", err, res.Stderr)
	}
	if res.Decision != DecisionAllow || res.ExitCode != 0 {
		t.Fatalf("result = %+v, want allow exit 0", res)
	}
}

func TestRunDeny(t *testing.T) {
	t.Parallel()
	reg, dir := testRegistry(t)
	hookFile(t, dir, "deny")
	res, err := reg.Run(context.Background(), "deny", EventGatewayInbound, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Decision != DecisionDeny || res.Reason != "blocked by test" {
		t.Fatalf("result = %+v, want deny with reason", res)
	}
}

func TestRunExitFailsClosed(t *testing.T) {
	t.Parallel()
	reg, dir := testRegistry(t)
	hookFile(t, dir, "exit")
	res, err := reg.Run(context.Background(), "exit", EventGatewayInbound, nil)
	if !errors.Is(err, ErrExit) {
		t.Fatalf("err = %v, want ErrExit", err)
	}
	if res.ExitCode != 3 || res.Stderr != "boom" {
		t.Fatalf("result = %+v, want non-zero exit and stderr", res)
	}
}

func TestRunTimeout(t *testing.T) {
	t.Parallel()
	reg, dir := testRegistry(t)
	reg.timeout = 100 * time.Millisecond
	hookFile(t, dir, "timeout")
	start := time.Now()
	res, err := reg.Run(context.Background(), "timeout", EventGatewayInbound, nil)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("timeout took %s", elapsed)
	}
	if res.ExitCode != -1 {
		t.Fatalf("exit code = %d, want -1", res.ExitCode)
	}
}

func TestRunTruncatesOutput(t *testing.T) {
	t.Parallel()
	reg, dir := testRegistry(t)
	reg.maxOutput = 32
	hookFile(t, dir, "truncate")
	res, err := reg.Run(context.Background(), "truncate", EventGatewayInbound, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Truncated || len(res.Stdout) != 32 {
		t.Fatalf("truncated=%v len=%d, want true/32", res.Truncated, len(res.Stdout))
	}
	if res.Decision != DecisionAllow {
		t.Fatalf("decision = %q, want allow", res.Decision)
	}
}

func TestRunEnvHasNoSecret(t *testing.T) {
	t.Parallel()
	reg, dir := testRegistry(t)
	hookFile(t, dir, "env")
	res, err := reg.Run(context.Background(), "env", EventGatewayInbound, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := "marker=,leak=,path=yes"
	if res.Reason != want {
		t.Fatalf("reason = %q, want %q", res.Reason, want)
	}
}

func TestRunRedactsKnownSecrets(t *testing.T) {
	t.Setenv("HOOK_TEST_SECRET", "redact-me-123456")
	reg, dir := testRegistry(t)
	hookFile(t, dir, "redact")
	res, err := reg.Run(context.Background(), "redact", EventGatewayInbound, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(res.Reason, "redact-me-123456") || !strings.Contains(res.Reason, redacted) {
		t.Fatalf("reason = %q, want redacted", res.Reason)
	}
}

func TestRunInvalidJSON(t *testing.T) {
	t.Parallel()
	reg, dir := testRegistry(t)
	hookFile(t, dir, "garbage")
	if _, err := reg.Run(context.Background(), "garbage", EventGatewayInbound, nil); !errors.Is(err, ErrResponse) {
		t.Fatalf("err = %v, want ErrResponse", err)
	}
}

func TestLookupRejectsPathTraversal(t *testing.T) {
	t.Parallel()
	reg, dir := testRegistry(t)
	if err := os.WriteFile(filepath.Join(filepath.Dir(dir), "outside"), []byte("#!/bin/sh\n"), 0o700); err != nil { // #nosec G306 -- traversal target
		t.Fatal(err)
	}
	for _, name := range []string{"../outside", "a/b", "..", ".hidden", "", strings.Repeat("a", 65), `a\b`} {
		if _, err := reg.Lookup(name); !errors.Is(err, ErrInvalidName) {
			t.Errorf("Lookup(%q) err = %v, want ErrInvalidName", name, err)
		}
	}
}

func TestListReportsProblems(t *testing.T) {
	t.Parallel()
	reg, dir := testRegistry(t)
	hookFile(t, dir, "allow")
	if err := os.WriteFile(filepath.Join(dir, "plain"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad name"), []byte("x"), 0o700); err != nil { // #nosec G306 -- invalid-name fixture
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink(filepath.Join(dir, "allow"), filepath.Join(dir, "link")); err != nil {
			t.Fatal(err)
		}
	}
	list, err := reg.List()
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]string, len(list))
	for _, h := range list {
		got[h.Name] = h.Problem
	}
	want := map[string]string{"allow": "", "plain": "not executable", "bad name": "invalid name"}
	if runtime.GOOS != "windows" {
		want["link"] = "symlink"
	}
	for name, problem := range want {
		if got[name] != problem {
			t.Errorf("%s problem = %q, want %q (all=%v)", name, got[name], problem, got)
		}
	}
}

func TestFireAggregates(t *testing.T) {
	t.Parallel()
	reg, dir := testRegistry(t)
	hookFile(t, dir, "allow")
	hookFile(t, dir, "deny")
	v, err := reg.Fire(context.Background(), EventGatewayInbound, map[string]any{
		"platform": "telegram", "chat_id": "1", "user_id": "2", "text": "hi",
	})
	if err != nil {
		t.Fatalf("Fire: %v", err)
	}
	if v.Decision != DecisionDeny || v.Hook != "deny" || len(v.Results) != 2 {
		t.Fatalf("verdict = %+v, want deny from hook deny after both ran", v)
	}

	empty, _ := testRegistry(t)
	v, err = empty.Fire(context.Background(), EventGatewayInbound, nil)
	if err != nil || v.Decision != DecisionAllow || v.Hook != "" {
		t.Fatalf("empty verdict = %+v err=%v, want allow", v, err)
	}
	if _, err := reg.Fire(context.Background(), Event("nope"), nil); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("unknown event err = %v, want ErrInvalidEvent", err)
	}
}

const redacted = "[REDACTED:"

// testRegistry returns a registry over a fresh directory with a deterministic
// parent environment: one secret-looking marker, one non-secret marker.
func testRegistry(t *testing.T) (*Registry, string) {
	t.Helper()
	dir := t.TempDir()
	reg := New(dir)
	reg.env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"HOOK_MARKER_SECRET=marker-secret-value",
		"DAMNBOT_TEST_LEAK=leak-value",
		"HOOK_TEST_SECRET=redact-me-123456",
	}
	return reg, dir
}

// hookFile copies the test binary to dir/name and returns the file name
// (with .exe on Windows, so Lookup accepts it there).
func hookFile(t *testing.T, dir, mode string) {
	t.Helper()
	src, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name := mode
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if runtime.GOOS != "windows" {
		if lerr := os.Link(src, filepath.Join(dir, name)); lerr == nil {
			return
		}
	}
	data, err := os.ReadFile(src) // #nosec G304 -- our own test binary
	if err != nil {
		t.Fatal(err)
	}
	tmp, err := os.CreateTemp(dir, ".hook-*")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tmp.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := tmp.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(tmp.Name(), 0o700); err != nil { // #nosec G302 -- fixture must be executable
		t.Fatal(err)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, name)); err != nil {
		t.Fatal(err)
	}
}
