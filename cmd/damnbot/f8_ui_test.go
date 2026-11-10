//go:build !minimal

package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/tui"
)

// TestF8UI010Pets covers the terminal mascot (UI-010): default off, adopt via
// /hatch, toggle/scale/off through /pet, and the animated frame used by the
// TUI status bar.
func TestF8UI010Pets(t *testing.T) {
	data := t.TempDir()
	cmds := f8Commands(t, data)
	owner := f8Ticket(gateway.RoleOwner)
	name := hatchPet(t, cmds, owner, data)
	togglePet(t, cmds, owner, name)
	checkPetFrames(t)
}

// hatchPet adopts one pet and returns its name after the /pet scale round-trip.
func hatchPet(t *testing.T, cmds *gateway.Commands, owner *gateway.Ticket, data string) string {
	t.Helper()
	ctx := context.Background()
	st := f8PetsState{}
	if err := f8Load(f8StorePath(data, "pets.json"), &st); err != nil {
		t.Fatal(err)
	}
	if st.Active != "" {
		t.Fatalf("pets default active = %q, want off", st.Active)
	}
	if got := cmds.Dispatch(ctx, owner, "/pet list"); !got.Handled {
		t.Fatalf("list = %+v", got)
	}
	if got := cmds.Dispatch(ctx, owner, "/hatch a tiny wizard"); !strings.Contains(got.Reply, "hatched pet-") {
		t.Fatalf("hatch = %q", got.Reply)
	}
	if err := f8Load(f8StorePath(data, "pets.json"), &st); err != nil {
		t.Fatal(err)
	}
	if st.Active == "" || len(st.Items) != 1 {
		t.Fatalf("after hatch = %+v", st)
	}
	if got := cmds.Dispatch(ctx, owner, "/pet scale "+st.Active+" large"); !strings.Contains(got.Reply, "scale") {
		t.Fatalf("scale = %q", got.Reply)
	}
	return st.Active
}

// togglePet proves /pet toggle flips the selected pet off and on, and that
// /pet off clears the selection.
func togglePet(t *testing.T, cmds *gateway.Commands, owner *gateway.Ticket, name string) {
	t.Helper()
	ctx := context.Background()
	if got := cmds.Dispatch(ctx, owner, "/pet toggle "+name); !strings.Contains(got.Reply, "pet off") {
		t.Fatalf("toggle off = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, owner, "/pet toggle "+name); !strings.Contains(got.Reply, "selected "+name) {
		t.Fatalf("toggle on = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, owner, "/pet off"); !strings.Contains(got.Reply, "pet off") {
		t.Fatalf("off = %q", got.Reply)
	}
}

// checkPetFrames proves the frame animates and honours scale.
func checkPetFrames(t *testing.T) {
	t.Helper()
	if tui.PetFrame("", "medium", time.Unix(0, 0)) != "" {
		t.Fatal("empty pet name rendered a frame")
	}
	first := tui.PetFrame("wizard", "small", time.Unix(0, 0))
	later := tui.PetFrame("wizard", "small", time.Unix(0, int64(time.Second)))
	if first == "" || first == later {
		t.Fatalf("frames %q and %q are not animated", first, later)
	}
	if big := tui.PetFrame("wizard", "large", time.Unix(0, 0)); len(big) <= len(first) {
		t.Fatalf("large scale %q is not larger than small %q", big, first)
	}
}

// TestF8UI011Personalities covers the opt-in persona preset (UI-011): the
// catalog is closed, an unknown name is refused, and the choice persists.
func TestF8UI011Personalities(t *testing.T) {
	data := t.TempDir()
	cmds := f8Commands(t, data)
	owner, user := f8Ticket(gateway.RoleOwner), f8Ticket(gateway.RoleUser)
	ctx := context.Background()
	if got := cmds.Dispatch(ctx, owner, "/personality"); !strings.Contains(got.Reply, "personality default") ||
		!strings.Contains(got.Reply, "mentor") {
		t.Fatalf("default = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, owner, "/personality wizardry"); !strings.Contains(got.Reply, "unknown preset") {
		t.Fatalf("unknown = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, user, "/personality mentor"); !strings.Contains(got.Reply, "owner-only") {
		t.Fatalf("user mutation = %q", got.Reply)
	}
	if got := cmds.Dispatch(ctx, owner, "/personality mentor"); !strings.Contains(got.Reply, "personality mentor") {
		t.Fatalf("set = %q", got.Reply)
	}
	fresh := f8Commands(t, data)
	if got := fresh.Dispatch(ctx, owner, "/personality"); !strings.Contains(got.Reply, "personality mentor") {
		t.Fatalf("persisted = %q", got.Reply)
	}
	if tui.PersonaLabel("mentor") == "" || tui.PersonaLabel("") != "" || tui.PersonaLabel("default") != "" {
		t.Fatal("persona label does not honour opt-in")
	}
}
