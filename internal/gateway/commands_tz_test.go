package gateway_test

import (
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/gateway"
)

// TestTimezoneCommand (PROV-03): /tz shows the identity zone, a valid IANA
// name is stored, "off" restores the host zone, an invalid name is refused,
// and /status reports the effective zone.
func TestTimezoneCommand(t *testing.T) {
	fake := &fakeRuntime{}
	c := gateway.NewCommands(&gateway.CommandOptions{Runtime: fake})
	tk := testTicket(gateway.RoleOwner)

	res := c.Dispatch(t.Context(), tk, "/tz")
	if !strings.Contains(res.Reply, "host") {
		t.Fatalf("/tz default = %q", res.Reply)
	}
	res = c.Dispatch(t.Context(), tk, "/tz Asia/Jakarta")
	if !strings.Contains(res.Reply, "Asia/Jakarta") || !strings.Contains(res.Reply, "WIB") {
		t.Fatalf("/tz set = %q", res.Reply)
	}
	if got := fake.tz[tk.Scope.Platform+":"+tk.Scope.UserID]; got != "Asia/Jakarta" {
		t.Fatalf("stored %q", got)
	}
	res = c.Dispatch(t.Context(), tk, "/status")
	if !strings.Contains(res.Reply, "tz Asia/Jakarta") {
		t.Fatalf("/status = %q", res.Reply)
	}
	res = c.Dispatch(t.Context(), tk, "/tz Mars/Olympus")
	if !strings.Contains(res.Reply, "unknown IANA zone") {
		t.Fatalf("/tz invalid = %q", res.Reply)
	}
	res = c.Dispatch(t.Context(), tk, "/tz off")
	if !strings.Contains(res.Reply, "host") {
		t.Fatalf("/tz off = %q", res.Reply)
	}
	if got := fake.tz[tk.Scope.Platform+":"+tk.Scope.UserID]; got != "" {
		t.Fatalf("off stored %q", got)
	}
}
