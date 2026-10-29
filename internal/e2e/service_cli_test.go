package e2e

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestServiceInstallLinuxSystemd runs the real binary against the machine's
// systemd user manager: install --enable (is-enabled true), status, then
// uninstall that leaves the data directory untouched.
//
// The unit file necessarily lands in the manager's own unit directory (the
// manager ignores a client XDG_CONFIG_HOME), so the test skips instead of
// disturbing an existing damnbot.service. The daemon itself starts with a
// temporary --data-dir and a free loopback port, never the profile data dir.
func TestServiceInstallLinuxSystemd(t *testing.T) {
	unitDir, unitPath := requireSystemdUserUnit(t)
	port := freeLoopbackPort(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	bind := fmt.Sprintf("127.0.0.1:%d", port)
	e := newEnv(t)
	install := e.ExecEnv([]string{"XDG_DATA_HOME=" + dataDir}, "service", "install",
		"--bind", bind, "--data-dir", dataDir, "--unit-dir", unitDir, "--enable")
	requireExit(t, install, "install")
	assertUnit(t, unitPath, bind, dataDir)
	if !unitEnabled(t) {
		t.Fatal("is-enabled is not enabled after install --enable")
	}

	sentinel := filepath.Join(dataDir, "keep.db")
	if err := os.WriteFile(sentinel, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	status := waitActive(t, e, dataDir, unitDir)
	if status.Code != 0 || !strings.Contains(status.Stdout, "state active") || !strings.Contains(status.Stdout, "enabled enabled") {
		t.Fatalf("status: code=%d\nstdout:\n%s\nstderr:\n%s", status.Code, status.Stdout, status.Stderr)
	}

	requireExit(t, e.Exec("service", "uninstall", "--unit-dir", unitDir), "uninstall")
	if _, err := os.Stat(unitPath); !os.IsNotExist(err) {
		t.Fatalf("unit file still present: %v", err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("uninstall removed the data directory: %v", err)
	}
	if unitEnabled(t) {
		t.Fatal("unit still enabled after uninstall")
	}
}

// requireSystemdUserUnit skips unless a clean, writable user manager exists and
// registers best-effort cleanup.
func requireSystemdUserUnit(t *testing.T) (unitDir, unitPath string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("systemd user units are Linux-only")
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		t.Skip("systemctl not installed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	t.Cleanup(cancel)
	if err := exec.CommandContext(ctx, "systemctl", "--user", "show-environment").Run(); err != nil {
		t.Skip("systemd user manager unavailable")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	unitDir = filepath.Join(home, ".config", "systemd", "user")
	unitPath = filepath.Join(unitDir, "damnbot.service")
	if _, serr := os.Stat(unitPath); serr == nil {
		t.Skipf("%s already exists; refusing to disturb a real deployment", unitPath)
	}
	if out, oerr := exec.CommandContext(ctx, "systemctl", "--user", "is-enabled", "damnbot.service").Output(); oerr == nil && strings.TrimSpace(string(out)) == "enabled" {
		t.Skip("damnbot.service is already enabled; refusing to disturb a real deployment")
	}
	t.Cleanup(func() { cleanupUnit(t, unitPath) })
	return unitDir, unitPath
}

// cleanupUnit is idempotent and runs even when an assertion fires.
func cleanupUnit(t *testing.T, unitPath string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, args := range [][]string{{"stop", "damnbot.service"}, {"disable", "damnbot.service"}, {"daemon-reload"}} {
		cmd := exec.CommandContext(ctx, "systemctl", append([]string{"--user"}, args...)...) // #nosec G204 -- fixed systemctl, literal args
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Logf("cleanup systemctl %v: %v: %s", args, err, out)
		}
	}
	if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
		t.Logf("cleanup remove %s: %v", unitPath, err)
	}
}

func assertUnit(t *testing.T, unitPath, bind, dataDir string) {
	t.Helper()
	unit, err := os.ReadFile(unitPath) // #nosec G304 -- path is the manager's unit dir
	if err != nil {
		t.Fatalf("read unit: %v", err)
	}
	for _, want := range []string{
		"ExecStart=", " serve --bind " + bind, "--data-dir " + dataDir,
		"Restart=on-failure", "RestartPreventExitStatus=78",
	} {
		if !strings.Contains(string(unit), want) {
			t.Fatalf("unit missing %q:\n%s", want, unit)
		}
	}
}

func unitEnabled(t *testing.T) bool {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "systemctl", "--user", "is-enabled", "damnbot.service").Output()
	return err == nil && strings.TrimSpace(string(out)) == "enabled"
}

func waitActive(t *testing.T, e *Env, dataDir, unitDir string) Result {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		status := e.Exec("service", "status", "--data-dir", dataDir, "--unit-dir", unitDir)
		if strings.Contains(status.Stdout, "state active") || time.Now().After(deadline) {
			return status
		}
		<-time.After(200 * time.Millisecond)
	}
}

func requireExit(t *testing.T, r Result, what string) {
	t.Helper()
	if r.Code != 0 {
		t.Fatalf("%s: code=%d\nstdout:\n%s\nstderr:\n%s", what, r.Code, r.Stdout, r.Stderr)
	}
}

// freeLoopbackPort reserves one loopback port for the duration of the test.
func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := 0
	if addr, ok := ln.Addr().(*net.TCPAddr); ok {
		port = addr.Port
	}
	if err := ln.Close(); err != nil {
		t.Fatalf("release port: %v", err)
	}
	if port == 0 {
		t.Fatal("reserved address is not TCP")
	}
	return port
}
