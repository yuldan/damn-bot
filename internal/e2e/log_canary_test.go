//go:build unix

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// scanLogContent returns every line at INFO or above that contains one of the
// canaries. The parse step is the point: only structured records count, so a
// random binary match cannot fail the gate.
func scanLogContent(t *testing.T, dataDir string, canaries []string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dataDir, "logs"))
	if err != nil {
		t.Fatalf("read logs: %v", err)
	}
	var hits []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".log") {
			continue
		}
		hits = append(hits, scanLogFile(t, filepath.Join(dataDir, "logs", entry.Name()), entry.Name(), canaries)...)
	}
	return hits
}

// scanLogFile scans one JSON-lines log and returns the offending records.
func scanLogFile(t *testing.T, path, name string, canaries []string) []string {
	t.Helper()
	raw, err := os.ReadFile(path) // #nosec G304 -- name comes from the harness log dir
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	var hits []string
	for line := range strings.SplitSeq(strings.TrimSpace(string(raw)), "\n") {
		if line == "" || !operatorLevel(t, name, line) {
			continue
		}
		for _, c := range canaries {
			if strings.Contains(line, c) {
				hits = append(hits, name+": "+line)
			}
		}
	}
	return hits
}

// operatorLevel reports whether one record is INFO, WARN or ERROR.
func operatorLevel(t *testing.T, name, line string) bool {
	t.Helper()
	var rec struct {
		Level string `json:"level"`
	}
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("%s: non-JSON log line %q: %v", name, line, err)
	}
	return rec.Level == "INFO" || rec.Level == "WARN" || rec.Level == "ERROR"
}

// logLines reports whether any JSON line of the named file contains want.
func logLines(t *testing.T, dataDir, name, want string) bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dataDir, "logs", name)) // #nosec G304 -- fixed log name
	if err != nil {
		return false
	}
	return strings.Contains(string(raw), want)
}

// TestServeLogCanary is GAP-SEC-11 (F4 channel: gateway + cron): twenty unique
// canary texts pass through the real serve binary, and no line at INFO or
// above (damnbot.log/serve.log) carries one. The same scanner is proved by
// TestServeLogCanaryNegativeControl, so a green result cannot be vacuous.
func TestServeLogCanary(t *testing.T) {
	const count = 20
	canaries := make([]string, count)
	replies := make([]providertest.Reply, count+2)
	for i := range canaries {
		canaries[i] = fmt.Sprintf("CANARY-pro-%02d-7f3a", i)
		replies[i] = providertest.Text("ok")
	}
	e := New(t, replies...)
	// The cron channel: one due job whose prompt holds the first canary. The
	// serve tick records cron metadata; the prompt itself must never reach an
	// INFO line, even when the executor fails for lack of a model.
	res := e.Exec("cron", "add", "--schedule", "* * * * *", "--prompt", canaries[0], "--data-dir", e.DataDir)
	if res.Code != 0 {
		t.Fatalf("cron add: code=%d stdout=%q stderr=%q", res.Code, res.Stdout, res.Stderr)
	}
	tg := NewFakeTG(t, FakeTGOptions{})
	p := startGatewayServe(t, e, tg)
	// Five canaries per message: the per-identity queue is 5 and twenty
	// separate pushes would be rejected by overflow policy. Every canary text
	// still crosses the real gateway, and the suite stays inside its timeout.
	for i := 0; i < count; i += 5 {
		tg.PushText(gatewayOwner, strings.Join(canaries[i:i+5], " "))
		want := i/5 + 1
		waitFor(t, 15*time.Second, "provider turn", func() bool { return len(e.Server.Requests()) >= want })
	}
	waitFor(t, 10*time.Second, "gateway inbound INFO line", func() bool {
		return logLines(t, e.DataDir, "serve.log", "gateway inbound")
	})
	gatewayStop(t, p)
	if !logLines(t, e.DataDir, "serve.log", "cron tick") {
		t.Fatal("cron channel did not log a tick; the canary test would miss it")
	}
	if hits := scanLogContent(t, e.DataDir, canaries); len(hits) != 0 {
		t.Fatalf("canary at INFO or above:\n%s", strings.Join(hits, "\n"))
	}
}

// TestServeLogCanaryNegativeControl: the servetest hook writes one unscrubbed
// INFO line with a canary; scanLogContent must find it. If this test passes,
// the zero-hit assertion of TestServeLogCanary is meaningful.
func TestServeLogCanaryNegativeControl(t *testing.T) {
	const canary = "CANARY-leak-control-8b1d"
	e := New(t)
	tg := NewFakeTG(t, FakeTGOptions{})
	env := append(gatewayServeEnv(e, tg), "DAMNBOT_SERVE_TEST_LOG_LEAK="+canary)
	p := startServe(t, e, binPaths.serve, env)
	waitFor(t, 10*time.Second, "injected leak line", func() bool {
		return logLines(t, e.DataDir, "serve.log", canary)
	})
	gatewayStop(t, p)
	hits := scanLogContent(t, e.DataDir, []string{canary})
	if len(hits) == 0 {
		t.Fatal("scanner missed the injected canary; the positive gate is vacuous")
	}
}
