//go:build !minimal

package main

import (
	"strings"
	"testing"
)

// TestWebListenConflict: the web listener and the Telegram webhook listener
// never share an address; the error names both and the two knobs.
func TestWebListenConflict(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		web      string
		opts     serveOptions
		conflict bool
	}{
		{"same loopback", "127.0.0.1:7878", serveOptions{telegramMode: "webhook", telegramWebhookListen: "127.0.0.1:7878"}, true},
		{"wildcard vs loopback", "0.0.0.0:7878", serveOptions{telegramMode: "webhook", telegramWebhookListen: "127.0.0.1:7878"}, true},
		{"loopback vs wildcard", "127.0.0.1:7878", serveOptions{telegramMode: "webhook", telegramWebhookListen: ":7878"}, true},
		{"ipv6 wildcard", "[::]:7878", serveOptions{telegramMode: "webhook", telegramWebhookListen: "127.0.0.1:7878"}, true},
		{"ipv6 loopback", "[::1]:7878", serveOptions{telegramMode: "webhook", telegramWebhookListen: "[::1]:7878"}, true},
		{"different port", "127.0.0.1:7878", serveOptions{telegramMode: "webhook", telegramWebhookListen: "127.0.0.1:7879"}, false},
		{"different host", "127.0.0.1:7878", serveOptions{telegramMode: "webhook", telegramWebhookListen: "127.0.0.2:7878"}, false},
		{"polling mode", "127.0.0.1:7878", serveOptions{telegramMode: "polling", telegramWebhookListen: "127.0.0.1:7878"}, false},
		{"unix web", "unix:<state_dir>/web.sock", serveOptions{telegramMode: "webhook", telegramWebhookListen: "127.0.0.1:7878"}, false},
	}
	for _, tc := range cases {
		err := webListenConflict(tc.web, &tc.opts)
		if tc.conflict {
			if err == nil {
				t.Errorf("%s: no conflict reported", tc.name)
				continue
			}
			if !strings.Contains(err.Error(), tc.web) || !strings.Contains(err.Error(), tc.opts.telegramWebhookListen) {
				t.Errorf("%s: error %q does not name both listeners", tc.name, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected conflict %v", tc.name, err)
		}
	}
}

// TestWebMinimalRefusal also exists in the minimal build, where it asserts the
// opposite answer; here the full build must link the UI.
func TestWebMinimalRefusal(t *testing.T) {
	t.Parallel()
	if !webAvailable() {
		t.Fatal("full build reports the web UI as unavailable")
	}
}
