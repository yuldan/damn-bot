package tui

import (
	"context"
	"strings"
	"sync"
	"testing"
)

type fakeBrowserControl struct {
	mu    sync.Mutex
	calls []string
	reply map[string]string
}

func (f *fakeBrowserControl) record(call string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	return f.reply[call]
}

func (f *fakeBrowserControl) BrowserStatus(context.Context, string) string {
	return f.record("status")
}

func (f *fakeBrowserControl) BrowserConnect(_ context.Context, _ string, engine string) string {
	return f.record("connect " + engine)
}

func (f *fakeBrowserControl) BrowserClose(context.Context, string) string {
	return f.record("close")
}

func (f *fakeBrowserControl) BrowserEngine(_ context.Context, _ string, engine string) string {
	return f.record("engine " + engine)
}

// TestF6SLASH084Browser (SLASH-084): `/browser` has status, connect, close and
// engine, never prints an endpoint, refuses a malformed call without touching
// the controller, and says module_unavailable when no controller is wired.
func TestF6SLASH084Browser(t *testing.T) {
	t.Parallel()
	m := slashApp(t, &Config{Model: "fake/model"})
	control := &fakeBrowserControl{reply: map[string]string{
		"status":         "browser: engine=cdp state=closed real_profile=on approved=no extension=off",
		"connect cdp":    "browser: connected browser: engine=cdp state=connected",
		"close":          "browser: closed browser: engine=cdp state=closed",
		"engine camofox": "browser: engine set to camofox; connect with /browser connect camofox",
	}}
	m.host.Browser = control

	runSlashText(t, m, "/browser status")
	if got := lastNotice(m); !strings.Contains(got, "engine=cdp") || strings.Contains(got, "ws://") {
		t.Fatalf("status notice = %q", got)
	}
	runSlashText(t, m, "/browser connect cdp")
	if got := lastNotice(m); !strings.Contains(got, "connected") {
		t.Fatalf("connect notice = %q", got)
	}
	runSlashText(t, m, "/browser close")
	if got := lastNotice(m); !strings.Contains(got, "closed") {
		t.Fatalf("close notice = %q", got)
	}
	runSlashText(t, m, "/browser engine camofox")
	if got := lastNotice(m); !strings.Contains(got, "camofox") {
		t.Fatalf("engine notice = %q", got)
	}

	before := len(control.calls)
	runSlashText(t, m, "/browser connect")
	if got := lastNotice(m); !strings.Contains(got, "usage: /browser connect <engine>") {
		t.Fatalf("missing-arg notice = %q", got)
	}
	runSlashText(t, m, "/browser switch cdp")
	if got := lastNotice(m); !strings.Contains(got, "usage: /browser") {
		t.Fatalf("unknown-subcommand notice = %q", got)
	}
	if len(control.calls) != before {
		t.Fatalf("malformed /browser reached the controller: %v", control.calls[before:])
	}

	m.host.Browser = nil
	runSlashText(t, m, "/browser status")
	if got := lastNotice(m); !strings.Contains(got, "module_unavailable") {
		t.Fatalf("unwired notice = %q", got)
	}
}
