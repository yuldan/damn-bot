package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/config"
)

// TestDoctorNetworkTelegramStatus (ADR-247): `doctor --network` lists the
// telegram egress entry and reports the status each [network] mode gives it:
// enabled normally and with background=false, disabled under offline.
func TestDoctorNetworkTelegramStatus(t *testing.T) {
	cfg := &config.Config{}
	cfg.Network.Background = true
	cases := []struct {
		name    string
		offline bool
		bg      bool
		want    string
	}{
		{"normal", false, true, "enabled"},
		{"background off", false, false, "enabled"},
		{"offline", true, true, "disabled (offline_mode)"},
	}
	for _, c := range cases {
		cfg.Network.Offline = c.offline
		cfg.Network.Background = c.bg
		var buf bytes.Buffer
		if code := sayEgress(cfg, false, &buf); code != 0 {
			t.Fatalf("%s: sayEgress exit %d", c.name, code)
		}
		out := buf.String()
		if !strings.Contains(out, "telegram") || !strings.Contains(out, "api.telegram.org") {
			t.Fatalf("%s: telegram entry missing:\n%s", c.name, out)
		}
		for line := range strings.SplitSeq(out, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "telegram") && !strings.Contains(line, c.want) {
				t.Fatalf("%s: telegram status line = %q, want %q", c.name, line, c.want)
			}
		}
	}
}
