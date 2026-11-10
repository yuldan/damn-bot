//go:build !minimal

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/skills"
)

func tokenFromReply(reply string) string {
	for f := range strings.FieldsSeq(reply) {
		if after, ok := strings.CutPrefix(f, "token="); ok {
			return after
		}
	}
	return ""
}

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
	if got := cmds.Dispatch(ctx, owner, "/pet toggle byte"); !strings.Contains(got.Reply, "selected byte") {
		t.Fatalf("toggle = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, owner, "/pet scale byte large"); !strings.Contains(got.Reply, "large") {
		t.Fatalf("scale = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, owner, "/pet off"); !strings.Contains(got.Reply, "off") {
		t.Fatalf("off = %q", got.Reply)
	}
}

func TestF8SLASH073Hatch(t *testing.T) {
	data := t.TempDir()
	cmds := f8Commands(t, data)
	owner := f8Ticket(gateway.RoleOwner)
	ctx := context.Background()
	got := cmds.Dispatch(ctx, owner, "/generate-pet a small robot")
	if !got.Handled || !strings.Contains(got.Reply, "hatched pet-") {
		t.Fatalf("hatch = %+v", got)
	}
	name := strings.Fields(got.Reply)[1]
	if got = cmds.Dispatch(ctx, owner, "/hatch a small robot"); !strings.Contains(got.Reply, name) {
		t.Fatalf("idempotent hatch = %q", got.Reply)
	}
	if got = cmds.Dispatch(ctx, owner, "/hatch --remote cat"); !strings.Contains(got.Reply, "module_unavailable") {
		t.Fatalf("remote hatch = %q", got.Reply)
	}
	if got = cmds.Dispatch(ctx, f8Ticket(gateway.RoleUser), "/hatch dog"); !strings.Contains(got.Reply, "owner-only") {
		t.Fatalf("user hatch = %q", got.Reply)
	}
	if _, err := os.Stat(f8StorePath(data, "pets.json")); err != nil {
		t.Fatalf("hatch did not persist: %v", err)
	}
}

func TestF8SLASH102Debug(t *testing.T) {
	data := t.TempDir()
	cmds := f8Commands(t, data)
	owner := f8Ticket(gateway.RoleOwner)
	ctx := context.Background()
	if got := cmds.Dispatch(ctx, f8Ticket(gateway.RoleUser), "/debug share"); !strings.Contains(got.Reply, "owner-only") {
		t.Fatalf("user share = %q", got.Reply)
	}
	got := cmds.Dispatch(ctx, owner, "/debug share")
	if !got.Handled || !strings.Contains(got.Reply, "debug shared") {
		t.Fatalf("share = %+v", got)
	}
	token := tokenFromReply(got.Reply)
	if token == "" {
		t.Fatalf("no token: %q", got.Reply)
	}
	path := strings.Fields(got.Reply)[2]
	if b, err := os.ReadFile(path); err != nil || !strings.Contains(string(b), "damnbot ") { // #nosec G304 -- fixture path
		t.Fatalf("artifact = %q, %v", b, err)
	}
	if got = cmds.Dispatch(ctx, owner, "/debug delete "+token); !strings.Contains(got.Reply, "debug deleted") {
		t.Fatalf("delete = %q", got.Reply)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("artifact survived: %v", err)
	}
	if got = cmds.Dispatch(ctx, owner, "/debug delete "+token); !strings.Contains(got.Reply, "already used") {
		t.Fatalf("second delete = %q", got.Reply)
	}
}

func TestF8SLASH079Curator(t *testing.T) {
	data := t.TempDir()
	old := time.Now().Add(-31 * 24 * time.Hour).Unix()
	if err := skills.AppendLedger(data, &skills.LedgerEntry{At: old, Name: "alpha", SHA256: "a", Source: "/s", Action: "indexed"}); err != nil {
		t.Fatal(err)
	}
	cmds := f8Commands(t, data)
	owner, user := f8Ticket(gateway.RoleOwner), f8Ticket(gateway.RoleUser)
	ctx := context.Background()
	if got := cmds.Dispatch(ctx, owner, "/curator status"); !strings.Contains(got.Reply, "paused=false") {
		t.Fatalf("status = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, user, "/curator run"); !strings.Contains(got.Reply, "owner-only") {
		t.Fatalf("user run = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, owner, "/curator run"); !strings.Contains(got.Reply, "proposal archive alpha") {
		t.Fatalf("run = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, owner, "/curator pin alpha"); !strings.Contains(got.Reply, "pinned alpha") {
		t.Fatalf("pin = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, owner, "/curator list-archived"); got.Handled != true || got.Reply != "" {
		t.Fatalf("list-archived = %+v", got)
	}
	if got := cmds.Dispatch(ctx, owner, "/curator pause"); !strings.Contains(got.Reply, "pause") {
		t.Fatalf("pause = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, owner, "/curator run"); !strings.Contains(got.Reply, "paused") {
		t.Fatalf("run paused = %q", got.Reply)
	}
}
