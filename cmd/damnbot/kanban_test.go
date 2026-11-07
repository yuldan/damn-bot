package main

import (
	"context"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gateway/render"
	"github.com/damnbot/damnbot/internal/kanban"
)

func TestF7SLASH080Kanban(t *testing.T) {
	t.Parallel()
	db, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: t.TempDir(), Kind: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	})
	store := kanban.New(db, t.TempDir())
	if _, err := store.InitBoard(t.Context(), "default", "owner"); err != nil {
		t.Fatal(err)
	}
	cmds := gateway.NewCommands(&gateway.CommandOptions{Kanban: &gatewayKanban{store: store}})
	ticket := func(role gateway.Role) *gateway.Ticket {
		return &gateway.Ticket{
			Identity: gateway.Identity{
				Platform: "telegram", ExternalID: "u", Role: role,
				Status: gateway.StatusAllowlisted, Strength: gateway.StrengthCryptographic,
			},
			Scope: gateway.Scope{Platform: "telegram", ChatID: "c", UserID: "u"},
		}
	}
	user, owner := ticket(gateway.RoleUser), ticket(gateway.RoleOwner)

	if got := cmds.Dispatch(t.Context(), user, "/kanban list"); !got.Handled || strings.Contains(got.Reply, "owner-only") {
		t.Fatalf("user read = %+v", got)
	}
	if got := cmds.Dispatch(t.Context(), user, "/kanban create --title nope"); !got.Handled || !strings.Contains(got.Reply, "owner-only") {
		t.Fatalf("user mutation = %+v", got)
	}
	if got := cmds.Dispatch(t.Context(), owner, "/kanban create --title yes"); !got.Handled || strings.Contains(got.Reply, "unknown") {
		t.Fatalf("owner mutation = %+v", got)
	}
	long := strings.Repeat("x", 9000)
	got := cmds.Dispatch(t.Context(), owner, "/kanban create --title "+long)
	if !got.Handled || render.Units(got.Reply) > 2*3800+2 {
		t.Fatalf("reply bound = %d units", render.Units(got.Reply))
	}
	if out := cmds.Dispatch(context.Background(), owner, "/kanban stats"); !out.Handled || out.Reply == "" {
		t.Fatalf("stats = %+v", out)
	}
}
