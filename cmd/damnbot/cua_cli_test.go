//go:build !minimal

package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/damnbot/damnbot/internal/config"
)

// writeCuaConfig writes one strict-config file for a cua CLI test.
func writeCuaConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil { // #nosec G304,G703 -- temp config fixture
		t.Fatal(err)
	}
	return path
}

func runCuaCLI(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = runCua(args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestCuaCLIDisabled(t *testing.T) {
	t.Parallel()
	cfg := writeCuaConfig(t, "[modules.computer_use]\nenabled = false\n")
	code, _, stderr := runCuaCLI(t, "status", "--config", cfg)
	if code != cuaExitUnavailable || !strings.Contains(stderr, "module_unavailable") {
		t.Fatalf("status = %d, stderr = %q", code, stderr)
	}
	if code, _, _ := runCuaCLI(t, "bogus"); code != cuaExitUsage {
		t.Fatalf("unknown subcommand = %d, want %d", code, cuaExitUsage)
	}
}

func cuaHelperConfig(t *testing.T, extra string) string {
	t.Helper()
	exe, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	return writeCuaConfig(t, fmt.Sprintf(`[modules.computer_use]
enabled = true
backend = "process"
command = [%s, "-test.run=TestCuaHelperProcess", "--"]
env = ["CUA_HELPER", "CUA_LOG"]
%s`, strconv.Quote(exe), extra))
}

// TestCuaHelperProcess is the external backend used by the CLI tests. It exits
// directly so the test framework never appends PASS bytes to stdout.
func TestCuaHelperProcess(*testing.T) {
	if os.Getenv("CUA_HELPER") != "1" {
		return
	}
	var args []string
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	if len(args) == 0 {
		os.Exit(2)
	}
	if path := os.Getenv("CUA_LOG"); path != "" {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) // #nosec G304,G703 -- fixture log
		if err != nil {
			os.Exit(1)
		}
		if _, werr := f.WriteString(strings.Join(args, " ") + "\n"); werr != nil {
			os.Exit(1)
		}
		if cerr := f.Close(); cerr != nil {
			os.Exit(1)
		}
	}
	var out []byte
	switch args[0] {
	case "screenshot":
		out = append([]byte("\x89PNG\r\n\x1a\n"), []byte("helper")...)
	case "type":
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			os.Exit(1)
		}
		out = []byte("typed " + string(data))
	case "permissions":
		out = []byte(`{"accessibility":true,"screen_recording":true}`)
	case "click", "key", "scroll", "move", "wait":
		out = []byte("ok")
	default:
		os.Exit(2)
	}
	if _, err := os.Stdout.Write(out); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestCuaCLIProcessBackend(t *testing.T) {
	t.Setenv("CUA_HELPER", "1")
	cfg := cuaHelperConfig(t, "")
	cuaProcessReadChecks(t, cfg)
	cuaProcessInputChecks(t, cfg)
}

func cuaProcessReadChecks(t *testing.T, cfg string) {
	t.Helper()
	if code, stdout, stderr := runCuaCLI(t, "status", "--config", cfg); code != cuaExitOK || !strings.Contains(stdout, "available=true") {
		t.Fatalf("status = %d, out = %q, err = %q", code, stdout, stderr)
	}
	shot := filepath.Join(t.TempDir(), "screen.png")
	code, stdout, stderr := runCuaCLI(t, "screenshot", "--config", cfg, "--out", shot)
	if code != cuaExitOK || !strings.Contains(stdout, "bytes ->") {
		t.Fatalf("screenshot = %d, out = %q, err = %q", code, stdout, stderr)
	}
	png, err := os.ReadFile(shot) // #nosec G304 -- screenshot under t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(png, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatalf("screenshot bytes = %q", png)
	}
}

func cuaProcessInputChecks(t *testing.T, cfg string) {
	t.Helper()
	cuaProcessLifecycleChecks(t, cfg)
	cuaProcessActionChecks(t, cfg)
}

func cuaProcessLifecycleChecks(t *testing.T, cfg string) {
	t.Helper()
	code, _, stderr := runCuaCLI(t, "start", "--config", cfg)
	if code != cuaExitDenied || !strings.Contains(stderr, "computer_use start backend=process") {
		t.Fatalf("unapproved start = %d, err = %q", code, stderr)
	}
	code, stdout, stderr := runCuaCLI(t, "start", "--config", cfg, "--yes")
	if code != cuaExitOK || !strings.Contains(stdout, "started") {
		t.Fatalf("approved start = %d, out = %q, err = %q", code, stdout, stderr)
	}
	code, _, stderr = runCuaCLI(t, "stop", "--config", cfg)
	if code != cuaExitDenied || !strings.Contains(stderr, "computer_use stop backend=process") {
		t.Fatalf("unapproved stop = %d, err = %q", code, stderr)
	}
	code, stdout, stderr = runCuaCLI(t, "stop", "--config", cfg, "--yes")
	if code != cuaExitOK || !strings.Contains(stdout, "stopped") {
		t.Fatalf("approved stop = %d, out = %q, err = %q", code, stdout, stderr)
	}
}

func cuaProcessActionChecks(t *testing.T, cfg string) {
	t.Helper()
	code, _, stderr := runCuaCLI(t, "click", "--config", cfg, "--x", "1", "--y", "2")
	if code != cuaExitDenied || !strings.Contains(stderr, "computer_use click at (1, 2)") {
		t.Fatalf("unapproved click = %d, err = %q", code, stderr)
	}
	code, stdout, stderr := runCuaCLI(t, "click", "--config", cfg, "--x", "1", "--y", "2", "--yes")
	if code != cuaExitOK || strings.TrimSpace(stdout) != "ok" {
		t.Fatalf("approved click = %d, out = %q, err = %q", code, stdout, stderr)
	}
	code, _, stderr = runCuaCLI(t, "type", "--config", cfg, "--text", "hunter2")
	if code != cuaExitDenied || strings.Contains(stderr, "hunter2") {
		t.Fatalf("unapproved type = %d, err = %q", code, stderr)
	}
	code, stdout, stderr = runCuaCLI(t, "type", "--config", cfg, "--text", "hunter2", "--yes")
	if code != cuaExitOK || !strings.Contains(stdout, "typed hunter2") {
		t.Fatalf("approved type = %d, out = %q, err = %q", code, stdout, stderr)
	}
}

func newCuaHTTPBackend(t *testing.T) (*httptest.Server, *atomic.Bool, *atomic.Bool) {
	t.Helper()
	var started, stopped atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			started.Store(true)
			w.WriteHeader(http.StatusNoContent)
		case "/stop":
			stopped.Store(true)
			w.WriteHeader(http.StatusNoContent)
		case "/screenshot":
			if _, err := w.Write([]byte("\x89PNG\r\n\x1a\nhttp")); err != nil {
				return
			}
		case "/act":
			if _, err := w.Write([]byte(`{"output":"acted"}`)); err != nil {
				return
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &started, &stopped
}

func cuaHTTPChecks(t *testing.T, cfg string, started, stopped *atomic.Bool) {
	t.Helper()
	code, stdout, stderr := runCuaCLI(t, "status", "--config", cfg)
	if code != cuaExitOK || !strings.Contains(stdout, "backend=http") || !started.Load() {
		t.Fatalf("status = %d, out = %q, err = %q started=%v", code, stdout, stderr, started.Load())
	}
	code, stdout, _ = runCuaCLI(t, "click", "--config", cfg, "--x", "3", "--y", "4", "--yes")
	if code != cuaExitOK || strings.TrimSpace(stdout) != "acted" {
		t.Fatalf("click = %d, out = %q", code, stdout)
	}
	code, _, stderr = runCuaCLI(t, "stop", "--config", cfg)
	if code != cuaExitDenied || stopped.Load() || !strings.Contains(stderr, "computer_use stop backend=http") {
		t.Fatalf("unapproved stop = %d, stopped=%v, err = %q", code, stopped.Load(), stderr)
	}
	code, _, _ = runCuaCLI(t, "stop", "--config", cfg, "--yes")
	if code != cuaExitOK || !stopped.Load() {
		t.Fatalf("stop = %d, stopped=%v", code, stopped.Load())
	}
}

func TestCuaCLIHTTPBackend(t *testing.T) {
	t.Parallel()
	srv, started, stopped := newCuaHTTPBackend(t)
	cfg := writeCuaConfig(t, `[modules.computer_use]
enabled = true
backend = "http"
endpoint = `+strconv.Quote(srv.URL)+"\n")
	cuaHTTPChecks(t, cfg, started, stopped)
}

func TestCuaCLIHTTPUnavailable(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := srv.URL
	srv.Close()
	cfg := writeCuaConfig(t, `[modules.computer_use]
enabled = true
backend = "http"
endpoint = `+strconv.Quote(base)+"\n")
	code, _, stderr := runCuaCLI(t, "status", "--config", cfg)
	if code != cuaExitUnavailable || !strings.Contains(stderr, "module_unavailable") {
		t.Fatalf("status = %d, err = %q", code, stderr)
	}
}

// TestF6CLI054CUASubcommands is the CLI-054 acceptance row: install, status,
// doctor, permissions and screen all work without a screen, and every
// mutating command refuses without --yes.
func TestF6CLI054CUASubcommands(t *testing.T) {
	dataHome := t.TempDir()
	// Every platform resolver (XDG, macOS Application Support, Windows
	// LOCALAPPDATA) must point at the same temporary home.
	t.Setenv("HOME", dataHome)
	t.Setenv("USERPROFILE", dataHome)
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("LOCALAPPDATA", dataHome)
	t.Setenv("APPDATA", dataHome)
	t.Setenv("CUA_HELPER", "1")
	log := filepath.Join(t.TempDir(), "cua.log")
	t.Setenv("CUA_LOG", log)
	cfg := cuaHelperConfig(t, "")
	name := "cua-driver"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	loader, err := config.New(config.OS())
	if err != nil {
		t.Fatal(err)
	}
	paths, err := loader.Paths(activeProfile())
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(paths.DataDir, "cua", name)
	source := filepath.Join(t.TempDir(), "cua-driver")
	if err := os.WriteFile(source, []byte("driver-v1"), 0o700); err != nil { // #nosec G306 -- fixture must look executable
		t.Fatal(err)
	}
	cuaInstallChecks(t, cfg, source, dest)
	cuaDoctorPermissionsChecks(t, cfg, log)
	cuaScreenChecks(t, cfg, log)
	// A missing driver is module_unavailable, not a fake success.
	absent := writeCuaConfig(t, fmt.Sprintf("[modules.computer_use]\nenabled = true\ncommand = [%s]\n",
		strconv.Quote(filepath.Join(t.TempDir(), "absent"))))
	if code, stdout, stderr := runCuaCLI(t, "doctor", "--config", absent); code != cuaExitUnavailable ||
		!strings.Contains(stdout, "check driver fail") {
		t.Fatalf("doctor absent = %d, out = %q, err = %q", code, stdout, stderr)
	}
}

func cuaInstallChecks(t *testing.T, cfg, source, dest string) {
	t.Helper()
	if code, _, stderr := runCuaCLI(t, "install", "--config", cfg, "--source", source); code != cuaExitDenied ||
		!strings.Contains(stderr, "computer_use install source=cua-driver") {
		t.Fatalf("unapproved install = %d, err = %q", code, stderr)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) { // #nosec G703 -- fixture path under XDG_DATA_HOME
		t.Fatalf("unapproved install wrote %s", dest)
	}
	wrong := strings.Repeat("a", 64)
	if code, _, stderr := runCuaCLI(t, "install", "--config", cfg, "--source", source, "--sha256", wrong, "--yes"); code != cuaExitFail ||
		!strings.Contains(stderr, "checksum") {
		t.Fatalf("checksum install = %d, err = %q", code, stderr)
	}
	entries, err := os.ReadDir(filepath.Dir(dest))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("partial install files: %v", entries)
	}
	if code, stdout, stderr := runCuaCLI(t, "install", "--config", cfg, "--source", source, "--yes"); code != cuaExitOK ||
		!strings.Contains(stdout, "installed") {
		t.Fatalf("install = %d, out = %q, err = %q", code, stdout, stderr)
	}
	if got, err := os.ReadFile(dest); err != nil || string(got) != "driver-v1" { // #nosec G304,G703 -- fixture path
		t.Fatalf("installed = %q, %v", got, err)
	}
	if code, stdout, _ := runCuaCLI(t, "install", "--config", cfg, "--source", source, "--yes"); code != cuaExitOK ||
		!strings.Contains(stdout, "already installed") {
		t.Fatalf("idempotent install = %d, out = %q", code, stdout)
	}
}

func cuaDoctorPermissionsChecks(t *testing.T, cfg, log string) {
	t.Helper()
	if code, stdout, stderr := runCuaCLI(t, "doctor", "--config", cfg); code != cuaExitOK ||
		!strings.Contains(stdout, "check driver ok") || !strings.Contains(stdout, "check permissions ok") {
		t.Fatalf("doctor = %d, out = %q, err = %q", code, stdout, stderr)
	}
	if code, stdout, _ := runCuaCLI(t, "permissions", "--config", cfg); code != cuaExitOK ||
		!strings.Contains(stdout, "read_only=true") {
		t.Fatalf("permissions = %d, out = %q", code, stdout)
	}
	if code, _, _ := runCuaCLI(t, "permissions", "--config", cfg, "--grant"); code != cuaExitUsage {
		t.Fatalf("permissions --grant = %d, want usage", code)
	}
	b, rerr := os.ReadFile(log) // #nosec G304,G703 -- fixture log
	if rerr != nil && !os.IsNotExist(rerr) {
		t.Fatal(rerr)
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
		// macOS reads TCC through the documented read-only probe; no other
		// command may spawn the backend.
		if line != "" && (runtime.GOOS != "darwin" || line != "permissions status --json") {
			t.Fatalf("read-only commands spawned %q (log %q)", line, b)
		}
	}
}

func cuaScreenChecks(t *testing.T, cfg, log string) {
	t.Helper()
	if code, _, stderr := runCuaCLI(t, "screen", "--config", cfg, "--out", filepath.Join(t.TempDir(), "s.png")); code != cuaExitDenied ||
		!strings.Contains(stderr, "computer_use screen backend=process") {
		t.Fatalf("unapproved screen = %d, err = %q", code, stderr)
	}
	shot := filepath.Join(t.TempDir(), "screen.png")
	code, stdout, stderr := runCuaCLI(t, "screen", "--config", cfg, "--out", shot, "--yes")
	if code != cuaExitOK || !strings.Contains(stdout, `untrusted source="computer_use:process"`) {
		t.Fatalf("screen = %d, out = %q, err = %q", code, stdout, stderr)
	}
	if png, err := os.ReadFile(shot); err != nil || !strings.HasPrefix(string(png), "\x89PNG") { // #nosec G304,G703 -- fixture path
		t.Fatalf("screen file = %q, %v", png, err)
	}
	b, rerr := os.ReadFile(log) // #nosec G304,G703 -- fixture log
	if rerr != nil || !strings.Contains(string(b), "screenshot") {
		t.Fatalf("approved screen did not reach the backend: %q, %v", b, rerr)
	}
}
