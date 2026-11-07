//go:build !minimal

package main

import (
	"strings"
	"testing"
	"time"
)

// TestF7CLI015WhatsApp proves the Cloud-first setup surface (CLI-015): setup
// offers only the Cloud API until the single ban-risk acknowledgement exists,
// the Web mode is refused without `--accept-ban-risk`, and the ack plus
// `system_notice{wa_ban_risk_ack}` are written exactly once.
func TestF7CLI015WhatsApp(t *testing.T) {
	file, _ := configTestHome(t, "[provider]\nmodel = \"openai/gpt-test\"\n")
	before := readText(t, file)

	checkWhatsAppMenuCloudOnly(t, file, before)
	checkWhatsAppWebRefused(t, file, before)
	ack := checkWhatsAppWebEnabled(t, file)
	checkWhatsAppWebIdempotent(t, file, ack)
	checkWhatsAppMenuOffersWeb(t)
	checkWhatsAppCloudPointer(t, file)
}

func checkWhatsAppMenuCloudOnly(t *testing.T, file, before string) {
	t.Helper()
	code, stdout, stderr := runArgs(t, "whatsapp", "setup")
	if code != 0 {
		t.Fatalf("setup menu = %d (%s)", code, stderr)
	}
	if !strings.Contains(stdout, "whatsapp_cloud") {
		t.Fatalf("setup does not offer the Cloud API: %s", stdout)
	}
	if strings.Contains(stdout, "whatsapp_web") {
		t.Fatalf("setup offers the Web mode before an acknowledgement: %s", stdout)
	}
	if got := readText(t, file); got != before {
		t.Fatalf("setup menu wrote the config:\n%s", got)
	}
}

func checkWhatsAppWebRefused(t *testing.T, file, before string) {
	t.Helper()
	code, _, stderr := runArgs(t, "whatsapp", "setup", "--mode", "whatsapp_web")
	if code != 2 || !strings.Contains(stderr, "accept-ban-risk") {
		t.Fatalf("web without ack = %d (%s)", code, stderr)
	}
	if got := readText(t, file); got != before {
		t.Fatalf("refused setup wrote the config:\n%s", got)
	}
	if code, _, _ := runArgs(t, "whatsapp", "setup", "--mode", "signal"); code != 2 {
		t.Fatalf("unknown mode = %d, want 2", code)
	}
}

func checkWhatsAppWebEnabled(t *testing.T, file string) string {
	t.Helper()
	code, stdout, stderr := runArgs(t, "whatsapp", "setup", "--mode", "whatsapp_web", "--accept-ban-risk")
	if code != 0 {
		t.Fatalf("web setup = %d (%s)", code, stderr)
	}
	if out := strings.ToLower(stdout + stderr); strings.Contains(out, "qr") || strings.Contains(out, "token") {
		t.Fatalf("setup printed a credential or QR: %s%s", stdout, stderr)
	}
	cfg := readText(t, file)
	if !strings.Contains(cfg, waWebSection) || !strings.Contains(cfg, "enabled = true") {
		t.Fatalf("config after setup:\n%s", cfg)
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

func checkWhatsAppWebIdempotent(t *testing.T, file, ack string) {
	t.Helper()
	if code, _, stderr := runArgs(t, "whatsapp", "setup", "--mode", "whatsapp_web", "--accept-ban-risk"); code != 0 {
		t.Fatalf("second setup = %d (%s)", code, stderr)
	}
	if got := ackFromConfig(t, readText(t, file)); got != ack {
		t.Fatalf("second setup rewrote ack %q -> %q", ack, got)
	}
	if n := ackEvents(t); n != 1 {
		t.Fatalf("second setup wrote %d audit events", n)
	}
}

func checkWhatsAppMenuOffersWeb(t *testing.T) {
	t.Helper()
	code, stdout, stderr := runArgs(t, "whatsapp", "setup")
	if code != 0 {
		t.Fatalf("setup menu after ack = %d (%s)", code, stderr)
	}
	if !strings.Contains(stdout, "whatsapp_web") {
		t.Fatalf("setup hides the acknowledged Web mode: %s", stdout)
	}
}

func checkWhatsAppCloudPointer(t *testing.T, file string) {
	t.Helper()
	before := readText(t, file)
	code, stdout, stderr := runArgs(t, "whatsapp", "setup", "--mode", "whatsapp_cloud")
	if code != 0 {
		t.Fatalf("cloud setup = %d (%s)", code, stderr)
	}
	if !strings.Contains(stdout, "whatsapp-cloud setup") {
		t.Fatalf("cloud mode does not point at `damnbot whatsapp-cloud setup`: %s", stdout)
	}
	if got := readText(t, file); got != before {
		t.Fatalf("cloud mode wrote the config:\n%s", got)
	}
}
