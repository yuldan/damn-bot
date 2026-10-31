//go:build unix

package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// TestServeTelegramSudoNeedsApproval is the F4 full check of GAP-SEC-14: a
// sudo-class command sent from the fake Telegram owner asks per command with
// the privileged flag in the preview, and denying the inline keyboard never
// executes it. The plain-shell subtest is the negative control: the same
// harness and approval path do run an allowed command, so "nothing ran" is
// evidence about the deny, not a broken runner.
func TestServeTelegramSudoNeedsApproval(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command string
	}{
		{"sudo", "sudo -n sh -c 'touch sudo-out'"},
		{"pkexec", "pkexec sh -c 'touch sudo-out'"},
	} {
		t.Run(tc.name+" denied", func(t *testing.T) {
			e := NewTrusted(t,
				ToolCallReply("c1", "shell", `{"command":`+strconv.Quote(tc.command)+`,"timeout_s":10}`),
				providertest.Text("done"))
			tg := NewFakeTG(t, FakeTGOptions{})
			p := startGatewayServe(t, e, tg)
			tg.PushText(gatewayOwner, "elevate")
			prompt, err := tg.WaitSend(gatewayOwner, func(r FakeTGRequest) bool { return r.Params["reply_markup"] != "" }, 15*time.Second)
			if err != nil {
				t.Fatalf("%v\nstderr:\n%s", err, p.stderr.String())
			}
			if !strings.Contains(prompt.Text, "flags: privileged") || !strings.Contains(prompt.Text, tc.command) {
				t.Fatalf("preview is not the privileged per-command card: %q", prompt.Text)
			}
			pushCallback(t, tg, &prompt, approvalData(t, &prompt, "deny"))
			if _, werr := tg.WaitEdit(gatewayOwner, func(r FakeTGRequest) bool { return r.Text == "done" }, 15*time.Second); werr != nil {
				t.Fatalf("%v\nstderr:\n%s", werr, p.stderr.String())
			}
			gatewayStop(t, p)
			if _, serr := os.Stat(filepath.Join(e.Workdir, "sudo-out")); !os.IsNotExist(serr) {
				t.Fatalf("denied sudo command executed: %v", serr)
			}
		})
	}

	t.Run("control allowed plain shell runs", func(t *testing.T) {
		e := NewTrusted(t,
			ToolCallReply("c1", "shell", `{"command":"touch plain-out","timeout_s":10}`),
			providertest.Text("done"))
		tg := NewFakeTG(t, FakeTGOptions{})
		p := startGatewayServe(t, e, tg)
		tg.PushText(gatewayOwner, "plain")
		prompt, err := tg.WaitSend(gatewayOwner, func(r FakeTGRequest) bool { return r.Params["reply_markup"] != "" }, 15*time.Second)
		if err != nil {
			t.Fatalf("%v\nstderr:\n%s", err, p.stderr.String())
		}
		pushCallback(t, tg, &prompt, approvalData(t, &prompt, "allow"))
		if _, werr := tg.WaitEdit(gatewayOwner, func(r FakeTGRequest) bool { return r.Text == "done" }, 15*time.Second); werr != nil {
			t.Fatalf("%v\nstderr:\n%s", werr, p.stderr.String())
		}
		gatewayStop(t, p)
		if _, serr := os.Stat(filepath.Join(e.Workdir, "plain-out")); serr != nil {
			t.Fatalf("control command did not run: %v", serr)
		}
	})
}

// TestServeTelegramNoSudoPassword: SUDO_PASSWORD in the serve environment is
// never inherited by the shell child and never reaches the log. The tool
// result is the child's own `env` output, so a leak would be visible both in
// the next provider request and in the transcript.
func TestServeTelegramNoSudoPassword(t *testing.T) {
	const canary = "SUDO_PASSWORD=CANARY-sudo-pw-4d2e"
	e := NewTrusted(t,
		ToolCallReply("c1", "shell", `{"command":"env","timeout_s":10}`),
		providertest.Text("done"))
	tg := NewFakeTG(t, FakeTGOptions{})
	env := append(gatewayServeEnv(e, tg), canary)
	p := startServe(t, e, binPaths.main, env)
	tg.PushText(gatewayOwner, "dump env")
	prompt, err := tg.WaitSend(gatewayOwner, func(r FakeTGRequest) bool { return r.Params["reply_markup"] != "" }, 15*time.Second)
	if err != nil {
		t.Fatalf("%v\nstderr:\n%s", err, p.stderr.String())
	}
	pushCallback(t, tg, &prompt, approvalData(t, &prompt, "allow"))
	if _, werr := tg.WaitEdit(gatewayOwner, func(r FakeTGRequest) bool { return r.Text == "done" }, 15*time.Second); werr != nil {
		t.Fatalf("%v\nstderr:\n%s", werr, p.stderr.String())
	}
	gatewayStop(t, p)
	for i, req := range e.Server.Requests() {
		if strings.Contains(string(req), "CANARY-sudo-pw-4d2e") {
			t.Fatalf("provider request %d carries SUDO_PASSWORD", i)
		}
	}
	for _, hit := range sudoLogHits(t, e.DataDir, "CANARY-sudo-pw-4d2e") {
		t.Fatalf("SUDO_PASSWORD in INFO log: %s", hit)
	}
}

// sudoLogHits returns structured log lines at INFO or above that carry needle.
func sudoLogHits(t *testing.T, dataDir, needle string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dataDir, "logs"))
	if err != nil {
		return nil
	}
	var hits []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".log") {
			continue
		}
		raw, rerr := os.ReadFile(filepath.Join(dataDir, "logs", entry.Name())) // #nosec G304 -- name comes from the harness log dir
		if rerr != nil {
			t.Fatalf("read %s: %v", entry.Name(), rerr)
		}
		for line := range strings.SplitSeq(strings.TrimSpace(string(raw)), "\n") {
			var rec struct {
				Level string `json:"level"`
			}
			if line == "" || json.Unmarshal([]byte(line), &rec) != nil {
				continue
			}
			if (rec.Level == "INFO" || rec.Level == "WARN" || rec.Level == "ERROR") && strings.Contains(line, needle) {
				hits = append(hits, entry.Name()+": "+line)
			}
		}
	}
	return hits
}
