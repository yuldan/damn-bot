package main

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// TestSetupNoWebWithoutAck is the GAP-GW-01 negative control: the wizard/CLI
// refuses the Web mode without the single `--accept-ban-risk` acknowledgement,
// writes no config, and with the flag writes exactly one ack and one audit
// event.
func TestSetupNoWebWithoutAck(t *testing.T) {
	file, _ := configTestHome(t, "[provider]\nmodel = \"openai/gpt-test\"\n")
	before := readText(t, file)
	runWAWebRefused(t, file, before)
	ack := runWAWebEnabled(t, file)
	runWAWebIdempotent(t, file, ack)
	runWAWebDisabled(t, file)
}

func runWAWebRefused(t *testing.T, file, before string) {
	t.Helper()
	code, _, stderr := runArgs(t, "gateway", "whatsapp-web", "enable")
	if code != 2 || !strings.Contains(stderr, "accept-ban-risk") {
		t.Fatalf("enable without ack = %d (%s)", code, stderr)
	}
	if got := readText(t, file); got != before {
		t.Fatalf("refused enable wrote config:\n%s", got)
	}
}

func runWAWebEnabled(t *testing.T, file string) string {
	t.Helper()
	code, stdout, stderr := runArgs(t, "gateway", "whatsapp-web", "enable", "--accept-ban-risk")
	if code != 0 {
		t.Fatalf("enable = %d (%s)", code, stderr)
	}
	if out := strings.ToLower(stdout + stderr); strings.Contains(out, "qr") || strings.Contains(out, "token") {
		t.Fatalf("enable printed a credential or QR: %s%s", stdout, stderr)
	}
	cfg := readText(t, file)
	if !strings.Contains(cfg, waWebSection) || !strings.Contains(cfg, "enabled = true") {
		t.Fatalf("config after enable:\n%s", cfg)
	}
	ack := ackFromConfig(t, cfg)
	if _, err := time.Parse(time.RFC3339, ack); err != nil {
		t.Fatalf("ack = %q: %v", ack, err)
	}
	if n := ackEvents(t); n != 1 {
		t.Fatalf("audit events = %d, want 1", n)
	}
	return ack
}

func runWAWebIdempotent(t *testing.T, file, ack string) {
	t.Helper()
	if code, _, stderr := runArgs(t, "gateway", "whatsapp-web", "enable", "--accept-ban-risk"); code != 0 {
		t.Fatalf("second enable = %d (%s)", code, stderr)
	}
	if got := ackFromConfig(t, readText(t, file)); got != ack {
		t.Fatalf("second enable rewrote ack %q → %q", ack, got)
	}
	if n := ackEvents(t); n != 1 {
		t.Fatalf("second enable wrote %d audit events", n)
	}
}

func runWAWebDisabled(t *testing.T, file string) {
	t.Helper()
	if code, _, stderr := runArgs(t, "gateway", "whatsapp-web", "disable"); code != 0 {
		t.Fatalf("disable = %d (%s)", code, stderr)
	}
	if cfg := readText(t, file); !strings.Contains(cfg, "enabled = false") {
		t.Fatalf("disable did not clear enabled:\n%s", cfg)
	}
}

// TestF7GAPGW01WizardModes locks the mode table: Cloud API is the only mode
// offered by default; Web appears only after the acknowledgement and is never
// the default choice.
func TestF7GAPGW01WizardModes(t *testing.T) {
	modes := whatsAppWizardModes(false)
	if len(modes) != 1 || modes[0].Name != "whatsapp_cloud" || modes[0].Web {
		t.Fatalf("default modes = %+v, want cloud only", modes)
	}
	if _, err := selectWhatsAppMode("whatsapp_web", false); !errors.Is(err, errWebNeedsAck) {
		t.Fatalf("web without ack = %v", err)
	}
	if _, err := selectWhatsAppMode("whatsapp_bogus", true); err == nil {
		t.Fatal("unknown mode accepted")
	}
	withAck := whatsAppWizardModes(true)
	if len(withAck) != 2 || withAck[1].Name != "whatsapp_web" || !withAck[1].NeedsAck {
		t.Fatalf("acked modes = %+v", withAck)
	}
	if mode, err := selectWhatsAppMode("whatsapp_web", true); err != nil || !mode.Web {
		t.Fatalf("web with ack = %+v/%v", mode, err)
	}
}

func readText(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	return string(b)
}

func ackFromConfig(t *testing.T, cfg string) string {
	t.Helper()
	for line := range strings.SplitSeq(cfg, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ack_ban_risk_at") {
			_, value, ok := strings.Cut(line, "=")
			if !ok {
				break
			}
			return strings.Trim(strings.TrimSpace(value), `"`)
		}
	}
	t.Fatalf("config has no ack_ban_risk_at:\n%s", cfg)
	return ""
}

func ackEvents(t *testing.T) int {
	t.Helper()
	db, err := openSessionsDB(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			return
		}
	}()
	events, err := db.History(context.Background(), ctxdb.SystemSession, 0)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for i := range events {
		if events[i].Kind == "system_notice" && strings.Contains(events[i].Payload, "wa_ban_risk_ack") {
			n++
		}
	}
	return n
}
