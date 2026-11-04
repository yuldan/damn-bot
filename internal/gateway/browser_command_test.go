package gateway_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/damnbot/damnbot/internal/gateway"
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

func (f *fakeBrowserControl) BrowserConnect(_ context.Context, sessionID, engine string) string {
	return f.record("connect " + engine)
}

func (f *fakeBrowserControl) BrowserClose(context.Context, string) string {
	return f.record("close")
}

func (f *fakeBrowserControl) BrowserEngine(_ context.Context, _ string, engine string) string {
	return f.record("engine " + engine)
}

// TestF6SLASH084Browser (SLASH-084) is the gateway half: the same four
// subcommands, owner-only, an honest module_unavailable without a controller,
// and a malformed call that never reaches it.
func TestF6SLASH084Browser(t *testing.T) {
	t.Parallel()
	control := &fakeBrowserControl{reply: map[string]string{
		"status":         "browser: engine=cdp state=closed real_profile=on approved=no extension=off",
		"connect cdp":    "browser: connected browser: engine=cdp state=connected",
		"close":          "browser: closed browser: engine=cdp state=closed",
		"engine camofox": "browser: engine set to camofox",
	}}
	c := gateway.NewCommands(&gateway.CommandOptions{Browser: control})
	ctx := context.Background()

	for _, tc := range []struct{ text, want string }{
		{"/browser", "engine=cdp"},
		{"/browser status", "state=closed"},
		{"/browser connect cdp", "connected"},
		{"/browser close", "closed"},
		{"/browser engine camofox", "engine set to camofox"},
	} {
		got := c.Dispatch(ctx, testTicket(gateway.RoleOwner), tc.text)
		if !got.Handled || !strings.Contains(got.Reply, tc.want) {
			t.Errorf("%s = %+v, want %q", tc.text, got, tc.want)
		}
		if strings.Contains(got.Reply, "ws://") {
			t.Errorf("%s printed an endpoint: %q", tc.text, got.Reply)
		}
	}

	before := len(control.calls)
	for _, text := range []string{"/browser connect", "/browser switch cdp", "/browser status extra"} {
		got := c.Dispatch(ctx, testTicket(gateway.RoleOwner), text)
		if !strings.Contains(got.Reply, "usage: /browser") {
			t.Errorf("%s = %q, want usage", text, got.Reply)
		}
	}
	if len(control.calls) != before {
		t.Fatalf("malformed /browser reached the controller: %v", control.calls[before:])
	}

	if got := c.Dispatch(ctx, testTicket(gateway.RoleUser), "/browser status"); !strings.Contains(got.Reply, "owner-only") {
		t.Fatalf("user /browser = %q, want owner-only", got.Reply)
	}

	unwired := gateway.NewCommands(&gateway.CommandOptions{})
	if got := unwired.Dispatch(ctx, testTicket(gateway.RoleOwner), "/browser status"); !strings.Contains(got.Reply, "module_unavailable") {
		t.Fatalf("unwired /browser = %q", got.Reply)
	}
}
