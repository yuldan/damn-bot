package gateway_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/gateway"
)

type fakeMCPReloader struct {
	n   int
	err error
}

func (f fakeMCPReloader) ReloadMCP(context.Context) (int, error) { return f.n, f.err }

// TestReloadMCPCommand: /reload-mcp is owner-only and reports the count; a
// nil reloader (minimal build) is unsupported, not silently successful.
func TestReloadMCPCommand(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		opts *gateway.CommandOptions
		role gateway.Role
		want string
	}{
		{name: "owner", opts: &gateway.CommandOptions{MCP: fakeMCPReloader{n: 2}}, role: gateway.RoleOwner, want: "2 server(s) reloaded"},
		{name: "user_denied", opts: &gateway.CommandOptions{MCP: fakeMCPReloader{n: 2}}, role: gateway.RoleUser, want: "owner-only"},
		{name: "unsupported", opts: &gateway.CommandOptions{}, role: gateway.RoleOwner, want: "not supported"},
		{name: "reload_error", opts: &gateway.CommandOptions{MCP: fakeMCPReloader{err: errors.New("boom")}}, role: gateway.RoleOwner, want: "reload failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cmds := gateway.NewCommands(tc.opts)
			res := cmds.Dispatch(context.Background(), testTicket(tc.role), "/reload-mcp")
			if !res.Handled || !strings.Contains(res.Reply, tc.want) {
				t.Fatalf("reply = %q (handled=%v), want %q", res.Reply, res.Handled, tc.want)
			}
		})
	}
}
