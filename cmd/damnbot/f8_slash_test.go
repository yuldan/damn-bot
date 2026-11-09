//go:build !minimal

package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/gateway"
)

func f8Ticket(role gateway.Role) *gateway.Ticket {
	return &gateway.Ticket{
		Identity: gateway.Identity{
			Platform: "telegram", ExternalID: "u", Role: role,
			Status: gateway.StatusAllowlisted, Strength: gateway.StrengthCryptographic,
		},
		Scope: gateway.Scope{Platform: "telegram", ChatID: "c", UserID: "u"},
	}
}

func f8Commands(t *testing.T, data string) *gateway.Commands {
	t.Helper()
	return gateway.NewCommands(&gateway.CommandOptions{F8: &f8SlashAdapter{dir: data}})
}

func TestF8SLASH077Suggestions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "cfg"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	data := filepath.Join(home, "d")
	cmds := f8Commands(t, data)
	owner, user := f8Ticket(gateway.RoleOwner), f8Ticket(gateway.RoleUser)
	ctx := context.Background()
	got := cmds.Dispatch(ctx, user, "/suggestions catalog")
	if !got.Handled || !strings.Contains(got.Reply, "suggestions off") || !strings.Contains(got.Reply, "AUTO-013") {
		t.Fatalf("catalog = %+v", got)
	}
	if got = cmds.Dispatch(ctx, user, "/suggest accept s1"); !strings.Contains(got.Reply, "owner-only") {
		t.Fatalf("user accept = %q", got.Reply)
	}
	st := f8Suggestions{Items: []f8Suggestion{{ID: "s1", Text: "try /loop", Status: "pending"}}}
	if err := f8Save(f8StorePath(data, "suggestions.json"), &st); err != nil {
		t.Fatal(err)
	}
	if got = cmds.Dispatch(ctx, owner, "/suggestions accept s1"); !strings.Contains(got.Reply, "s1 accepted") {
		t.Fatalf("accept = %q", got.Reply)
	}
	if got = cmds.Dispatch(ctx, owner, "/suggestions dismiss s1"); !strings.Contains(got.Reply, "s1 dismissed") {
		t.Fatalf("dismiss = %q", got.Reply)
	}
	if got = cmds.Dispatch(ctx, owner, "/suggestions clear"); !strings.Contains(got.Reply, "cleared") {
		t.Fatalf("clear = %q", got.Reply)
	}
}

func TestF8SLASH078Blueprint(t *testing.T) {
	cmds := f8Commands(t, t.TempDir())
	owner := f8Ticket(gateway.RoleOwner)
	ctx := context.Background()
	if got := cmds.Dispatch(ctx, owner, "/blueprint catalog"); !strings.Contains(got.Reply, "daily-review") {
		t.Fatalf("catalog = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, owner, "/bp pr-watch repo=acme/x interval=10m"); !strings.Contains(got.Reply, "blueprint pr-watch ready") {
		t.Fatalf("instantiate = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, owner, "/blueprint pr-watch repo=acme/x interval=10m nope=1"); !strings.Contains(got.Reply, "unknown slot nope") {
		t.Fatalf("unknown slot = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, owner, "/blueprint pr-watch repo=acme/x"); !strings.Contains(got.Reply, "missing slot interval") {
		t.Fatalf("missing slot = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, owner, "/blueprint absent"); !strings.Contains(got.Reply, "unknown template") {
		t.Fatalf("unknown template = %q", got.Reply)
	}
}

func TestF8SLASH030Goal(t *testing.T) {
	data := t.TempDir()
	cmds := f8Commands(t, data)
	owner, user := f8Ticket(gateway.RoleOwner), f8Ticket(gateway.RoleUser)
	ctx := context.Background()
	if got := cmds.Dispatch(ctx, user, "/goal ship the release"); !strings.Contains(got.Reply, "owner-only") {
		t.Fatalf("user set = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, owner, "/goal ship the release"); !strings.Contains(got.Reply, "goal set") {
		t.Fatalf("set = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, owner, "/goal status"); !strings.Contains(got.Reply, "ship the release") {
		t.Fatalf("status = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, owner, "/goal"); !strings.Contains(got.Reply, "goal turns=") {
		t.Fatalf("bare status = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, owner, "/goal clear"); !strings.Contains(got.Reply, "cleared") {
		t.Fatalf("clear = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, owner, "/goal clear"); !strings.Contains(got.Reply, "none") {
		t.Fatalf("idempotent clear = %q", got.Reply)
	}
}

func TestF8SLASHNilSeam(t *testing.T) {
	cmds := gateway.NewCommands(&gateway.CommandOptions{})
	got := cmds.Dispatch(context.Background(), f8Ticket(gateway.RoleOwner), "/goal x")
	if !got.Handled || !strings.Contains(got.Reply, "module_unavailable") {
		t.Fatalf("nil seam = %+v", got)
	}
}

func TestF8SlashStoreMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		return
	}
	data := t.TempDir()
	cmds := f8Commands(t, data)
	_ = cmds.Dispatch(context.Background(), f8Ticket(gateway.RoleOwner), "/goal ship")
	fi, err := os.Stat(f8StorePath(data, "slash-goals.json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("store mode = %v, %v", fi, err)
	}
}
