//go:build windows

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestWindowsTaskSchedulerRoundTrip is the real-schtasks contract on the
// Windows CI runner (no skip): install registers a per-profile task, the
// scheduler's own XML decodes strictly (EC-WIN-006), a second install is
// idempotent, status does not claim a supervisor without evidence
// (EC-WIN-003), uninstall retires the task and leaves the data dir untouched
// (EC-WIN-004).
func TestWindowsTaskSchedulerRoundTrip(t *testing.T) {
	profile := fmt.Sprintf("svcwin-%d-%d", os.Getpid(), time.Now().UnixNano()%1_000_000)
	ctx := context.Background()
	d, err := windowsServiceDeps(profile)
	if err != nil {
		t.Fatalf("deps: %v", err)
	}
	name := winTaskName(profile)
	if exists, qerr := winTaskExists(ctx, d, name); qerr != nil {
		t.Fatalf("pre-query: %v", qerr)
	} else if exists {
		t.Fatalf("test task %s already exists", name)
	}
	t.Cleanup(func() {
		var out, errb bytes.Buffer
		if code := winUninstallCmd(ctx, d, nil, &out, &errb); code != 0 {
			t.Errorf("cleanup uninstall: code=%d stderr=%q", code, errb.String())
		}
	})
	dataDir := filepath.Join(t.TempDir(), "data")
	if merr := os.MkdirAll(dataDir, 0o700); merr != nil {
		t.Fatal(merr)
	}
	sentinel := filepath.Join(dataDir, "keep.db")
	if werr := os.WriteFile(sentinel, []byte("x"), 0o600); werr != nil {
		t.Fatal(werr)
	}
	const bind = "127.0.0.1:17879"
	var stdout, stderr bytes.Buffer
	o := winInstallOptions{bind: bind, dataDir: dataDir}
	if code := winInstallCmd(ctx, d, winInstallArgs(o), &stdout, &stderr); code != 0 {
		t.Fatalf("install: code=%d stderr=%q", code, stderr.String())
	}
	if exists, qerr := winTaskExists(ctx, d, name); qerr != nil || !exists {
		t.Fatalf("task not registered: exists=%v err=%v", exists, qerr)
	}
	raw, qerr := windowsSchtasks(ctx, "/Query", "/TN", name, "/XML")
	if qerr != nil {
		t.Fatalf("query XML: %v", qerr)
	}
	text, derr := decodeTaskOutput(raw)
	if derr != nil {
		t.Fatalf("decode scheduler XML: %v", derr)
	}
	// schtasks re-serializes element text and keeps quotes literal, while our
	// staged document escapes them; compare the operator-visible action.
	for _, want := range []string{
		"<Command>" + d.base.exe + "</Command>",
		"<Arguments>serve --bind " + bind + " --data-dir \"" + dataDir + "\"</Arguments>",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("registered task missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(strings.ToLower(text), "api_token") {
		t.Errorf("registered task carries a secret:\n%s", text)
	}
	stdout.Reset()
	if code := winInstallCmd(ctx, d, winInstallArgs(o), &stdout, &stderr); code != 0 {
		t.Fatalf("second install: code=%d stderr=%q", code, stderr.String())
	}
	stdout.Reset()
	if code := winStatusCmd(ctx, d, []string{"--data-dir", dataDir}, &stdout, &stderr); code != 0 {
		t.Fatalf("status: code=%d stderr=%q", code, stderr.String())
	}
	for _, want := range []string{"task " + name, "registered yes", "state idle", "supervised none"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("status missing %q:\n%s", want, stdout.String())
		}
	}
	stdout.Reset()
	if code := winLogsCmd(ctx, d, []string{"--data-dir", dataDir}, &stdout, &stderr); code != 0 {
		t.Fatalf("logs: code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), name) {
		t.Errorf("logs do not mention the task:\n%s", stdout.String())
	}
	stdout.Reset()
	if code := winUninstallCmd(ctx, d, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("uninstall: code=%d stderr=%q", code, stderr.String())
	}
	if exists, qerr := winTaskExists(ctx, d, name); qerr != nil || exists {
		t.Fatalf("task still registered after uninstall: exists=%v err=%v", exists, qerr)
	}
	if _, serr := os.Stat(sentinel); serr != nil {
		t.Fatalf("uninstall removed data: %v", serr)
	}
}

// TestWindowsSupervisionProbe checks the real process probe used by status:
// the current process is alive, and ancestry lookup answers without error.
func TestWindowsSupervisionProbe(t *testing.T) {
	live, err := windowsProcessAlive(os.Getpid())
	if err != nil || !live {
		t.Fatalf("current process probe: live=%v err=%v", live, err)
	}
	if _, herr := windowsTaskHosted(os.Getpid()); herr != nil {
		t.Fatalf("ancestry probe: %v", herr)
	}
	if dead, derr := windowsProcessAlive(0x7ffffffe); derr != nil || dead {
		t.Fatalf("absent pid probe: alive=%v err=%v", dead, derr)
	}
}
