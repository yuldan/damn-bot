//go:build !minimal

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/gateway"
)

func TestF8SLASH027Journey(t *testing.T) {
	data := t.TempDir()
	seedJourney(t, data)
	cmds := f8Commands(t, data)
	owner, user := f8Ticket(gateway.RoleOwner), f8Ticket(gateway.RoleUser)
	ctx := context.Background()
	for _, name := range []string{"/journey", "/learning", "/memory-graph"} {
		got := cmds.Dispatch(ctx, owner, name)
		if !got.Handled || !strings.Contains(got.Reply, "beta") {
			t.Fatalf("%s = %+v", name, got)
		}
	}
	if got := cmds.Dispatch(ctx, user, "/journey delete x"); !strings.Contains(got.Reply, "owner-only") {
		t.Fatalf("user delete = %q", got.Reply)
	}
	got := cmds.Dispatch(ctx, owner, "/journey list")
	if units := len([]rune(got.Reply)); units > 3800 {
		t.Fatalf("journey reply %d runes over cap", units)
	}
}

func TestF8SLASH072Pet(t *testing.T) {
	data := t.TempDir()
	asset := filepath.Join(t.TempDir(), "pet.bin")
	if err := os.WriteFile(asset, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := runF8CLI(t, runPets, "--data-dir", data, "install", "byte", "--from", asset); code != f8ExitOK {
		t.Fatalf("install = %d", code)
	}
	cmds := f8Commands(t, data)
	owner, user := f8Ticket(gateway.RoleOwner), f8Ticket(gateway.RoleUser)
	ctx := context.Background()
	if got := cmds.Dispatch(ctx, owner, "/pet list"); !strings.Contains(got.Reply, "byte") {
		t.Fatalf("list = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, user, "/pet toggle byte"); !strings.Contains(got.Reply, "owner-only") {
		t.Fatalf("user toggle = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, owner, "/pet toggle byte"); !strings.Contains(got.Reply, "pet byte") {
		t.Fatalf("toggle = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, owner, "/pet scale byte large"); !strings.Contains(got.Reply, "large") {
		t.Fatalf("scale = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, owner, "/pet off"); !strings.Contains(got.Reply, "off") {
		t.Fatalf("off = %q", got.Reply)
	}
}
