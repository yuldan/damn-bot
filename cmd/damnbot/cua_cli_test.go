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
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
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
env = ["CUA_HELPER"]
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

func TestCuaCLIHTTPBackend(t *testing.T) {
	t.Parallel()
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
	defer srv.Close()
	cfg := writeCuaConfig(t, `[modules.computer_use]
enabled = true
backend = "http"
endpoint = `+strconv.Quote(srv.URL)+"\n")
	code, stdout, stderr := runCuaCLI(t, "status", "--config", cfg)
	if code != cuaExitOK || !strings.Contains(stdout, "backend=http") || !started.Load() {
		t.Fatalf("status = %d, out = %q, err = %q started=%v", code, stdout, stderr, started.Load())
	}
	code, stdout, _ = runCuaCLI(t, "click", "--config", cfg, "--x", "3", "--y", "4", "--yes")
	if code != cuaExitOK || strings.TrimSpace(stdout) != "acted" {
		t.Fatalf("click = %d, out = %q", code, stdout)
	}
	if code, _, _ = runCuaCLI(t, "stop", "--config", cfg); code != cuaExitOK || !stopped.Load() {
		t.Fatalf("stop = %d, stopped=%v", code, stopped.Load())
	}
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
