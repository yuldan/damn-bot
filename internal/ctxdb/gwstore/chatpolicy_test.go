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

// TestHomeChannelRoundTrip: GW-005 stores one home channel idempotently,
// including its topic, and rejects malformed ids.
func TestHomeChannelRoundTrip(t *testing.T) {
	store := Store{DB: open(t)}
	ctx := t.Context()
	if _, _, _, found, err := store.HomeChannel(ctx); err != nil || found {
		t.Fatalf("initial home = (found %v, %v), want absent", found, err)
	}
	for range 2 {
		if err := store.SetHomeChannel(ctx, "telegram", "-100123", "42", 10); err != nil {
			t.Fatal(err)
		}
	}
	p, c, th, found, err := store.HomeChannel(ctx)
	if err != nil || !found || p != "telegram" || c != "-100123" || th != "42" {
		t.Fatalf("home = (%s,%s,%s,%v,%v)", p, c, th, found, err)
	}
	if n := count(t, store.DB, "SELECT count(*) FROM module_state WHERE module_id = 'gateway'"); n != 1 {
		t.Fatalf("module_state rows = %d, want 1", n)
	}
	if err := store.SetHomeChannel(ctx, "", "c", "", 20); err == nil {
		t.Fatal("accepted a home channel without platform")
	}
}

// TestChatSettingIsScoped: GW-007/GW-020/GW-021 rows are per key and per chat.
func TestChatSettingIsScoped(t *testing.T) {
	store := Store{DB: open(t)}
	ctx := t.Context()
	if err := store.SetChatSetting(ctx, "telegram", "c1", "footer", "on", 10); err != nil {
		t.Fatal(err)
	}
	if err := store.SetChatSetting(ctx, "telegram", "c1", "timestamps", "true", 11); err != nil {
		t.Fatal(err)
	}
	if v, ok, err := store.ChatSetting(ctx, "telegram", "c1", "footer"); err != nil || !ok || v != "on" {
		t.Fatalf("footer = (%q, %v, %v)", v, ok, err)
	}
	if _, ok, err := store.ChatSetting(ctx, "telegram", "c2", "footer"); err != nil || ok {
		t.Fatalf("sibling chat footer found=%v err=%v", ok, err)
	}
	if v, ok, err := store.ChatSetting(ctx, "telegram", "c1", "timestamps"); err != nil || !ok || v != "true" {
		t.Fatalf("timestamps = (%q, %v, %v)", v, ok, err)
	}
}
