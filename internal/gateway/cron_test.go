package gateway_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/cron"
	"github.com/damnbot/damnbot/internal/gateway"
)

// TestCronCommandOwnerOnly is SLASH-076: a user role is refused before the
// cron surface is consulted; the owner never reaches the LLM (Dispatch only).
func TestCronCommandOwnerOnly(t *testing.T) {
	t.Parallel()
	fc := &fakeRuntime{cronJobs: []cron.Job{{ID: "j1", Name: "daily", Schedule: "0 9 * * *", Enabled: true}}}
	c := gateway.NewCommands(&gateway.CommandOptions{Runtime: fc})

	res := c.Dispatch(context.Background(), ticketIn(gateway.RoleUser, "u1"), "/cron list")
	if !res.Handled || !strings.Contains(res.Reply, "owner-only") {
		t.Fatalf("user /cron = %+v, want owner-only", res)
	}
	if len(fc.cronOps) != 0 {
		t.Fatalf("user /cron touched the store: %v", fc.cronOps)
	}

	res = c.Dispatch(context.Background(), ticketIn(gateway.RoleOwner, "o1"), "/cron list")
	if !res.Handled || !strings.Contains(res.Reply, "j1") || !strings.Contains(res.Reply, "active") {
		t.Fatalf("owner /cron list = %+v", res)
	}
}

// TestCronCommandMutations: each owner subcommand maps to exactly one store
// call, and a failed store answers without claiming success.
func TestCronCommandMutations(t *testing.T) {
	t.Parallel()
	fc := &fakeRuntime{}
	c := gateway.NewCommands(&gateway.CommandOptions{Runtime: fc})
	owner := ticketIn(gateway.RoleOwner, "o1")
	for _, sub := range []string{"pause", "resume", "run", "rm"} {
		res := c.Dispatch(context.Background(), owner, "/cron "+sub+" j7")
		if !res.Handled || !strings.Contains(res.Reply, "ok") {
			t.Fatalf("/cron %s = %+v", sub, res)
		}
	}
	for _, call := range []struct{ text, want string }{
		{"/cron add 0 9 * * * daily prompt", "cron add: ok (new-id)"},
		{"/cron edit j7 prompt new prompt", "cron edit: ok (j7)"},
	} {
		if res := c.Dispatch(context.Background(), owner, call.text); !strings.Contains(res.Reply, call.want) {
			t.Fatalf("%s = %+v", call.text, res)
		}
	}
	want := []string{"pause j7", "resume j7", "run j7", "rm j7", "add 0 9 * * * daily prompt", "edit j7 prompt new prompt"}
	if strings.Join(fc.cronOps, ",") != strings.Join(want, ",") {
		t.Fatalf("ops = %v, want %v", fc.cronOps, want)
	}

	fc.cronErr = errors.New("boom")
	if res := c.Dispatch(context.Background(), owner, "/cron pause j7"); !strings.Contains(res.Reply, "failed") {
		t.Fatalf("failed pause = %+v", res)
	}
}

// TestCronCommandWithoutRuntime: a nil cron seam is reported, never a fake
// success.
func TestCronCommandWithoutRuntime(t *testing.T) {
	t.Parallel()
	c := gateway.NewCommands(&gateway.CommandOptions{})
	res := c.Dispatch(context.Background(), ticketIn(gateway.RoleOwner, "o1"), "/cron list")
	if !res.Handled || !strings.Contains(res.Reply, "not supported") {
		t.Fatalf("/cron without seam = %+v", res)
	}
}
