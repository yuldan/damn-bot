package toolset

import (
	"slices"
	"testing"
)

// TestF7TSET036BotRoomBundle: `bot_room` is a composite bundle of room tools
// plus read-only core access. It contains no shell, write, or edit tool, and
// it resolves without any terminal capability.
func TestF7TSET036BotRoomBundle(t *testing.T) {
	t.Parallel()
	def := builtin("bot_room")
	if def.Name != "bot_room" || len(def.Tools) == 0 {
		t.Fatalf("bot_room = %+v", def)
	}
	want := []string{"room_send", "room_history", "room_members", "read_file", "glob", "grep", "session_search"}
	for _, name := range want {
		if !slices.Contains(def.Tools, name) {
			t.Errorf("bot_room is missing %q: %v", name, def.Tools)
		}
	}
	for _, banned := range []string{"shell", "write_file", "edit_file", "terminal"} {
		if slices.Contains(def.Tools, banned) {
			t.Errorf("bot_room exposes banned tool %q", banned)
		}
	}
	sel, err := Resolve(Config{Active: []string{"bot_room"}}, "linux", available(want...))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sel.Tools, []string{"glob", "grep", "read_file", "room_history", "room_members", "room_send", "session_search"}) {
		t.Fatalf("resolved bot_room = %v", sel.Tools)
	}
	if len(sel.Unavailable) != 0 {
		t.Fatalf("unavailable = %v", sel.Unavailable)
	}
}
