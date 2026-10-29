package gwstore

import "testing"

// TestChatOverrideRoundTrip: the temporary module_state row is absent by
// default, idempotent per (platform, chat), and scoped to that chat.
func TestChatOverrideRoundTrip(t *testing.T) {
	store := Store{DB: open(t)}
	ctx := t.Context()
	if value, found, err := store.ChatOverride(ctx, "telegram", "-100123"); err != nil || found || value != "" {
		t.Fatalf("missing override = (%q, %v, %v), want absent", value, found, err)
	}
	for range 2 {
		if err := store.SetChatOverride(ctx, "telegram", "-100123", "false", 10); err != nil {
			t.Fatal(err)
		}
	}
	if value, found, err := store.ChatOverride(ctx, "telegram", "-100123"); err != nil || !found || value != "false" {
		t.Fatalf("override = (%q, %v, %v), want false/true", value, found, err)
	}
	if _, found, err := store.ChatOverride(ctx, "telegram", "-100456"); err != nil || found {
		t.Fatalf("sibling chat = (found %v, %v), want absent", found, err)
	}
	if _, found, err := store.ChatOverride(ctx, "irc", "-100123"); err != nil || found {
		t.Fatalf("sibling platform = (found %v, %v), want absent", found, err)
	}
	if n := count(t, store.DB, "SELECT count(*) FROM module_state WHERE module_id = 'gateway'"); n != 1 {
		t.Fatalf("module_state rows = %d, want 1", n)
	}
}

// TestChatOverrideClear: an empty value clears the override; it reads back as
// absent so the gateway applies the default again.
func TestChatOverrideClear(t *testing.T) {
	store := Store{DB: open(t)}
	ctx := t.Context()
	if err := store.SetChatOverride(ctx, "telegram", "-100123", "true", 10); err != nil {
		t.Fatal(err)
	}
	if err := store.SetChatOverride(ctx, "telegram", "-100123", "", 20); err != nil {
		t.Fatal(err)
	}
	if value, found, err := store.ChatOverride(ctx, "telegram", "-100123"); err != nil || found || value != "" {
		t.Fatalf("after clear = (%q, %v, %v), want absent", value, found, err)
	}
}
