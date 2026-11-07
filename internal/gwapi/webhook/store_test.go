package webhook

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"
)

func testStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "webhook", "subscriptions.json")
	store, err := OpenStore(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	return store, path
}

// TestF7WebhookStore: the subscription table is persistent, idempotent on the
// same target, rejects unsafe targets, and stores secret names only.
func TestF7WebhookStore(t *testing.T) {
	t.Parallel()
	store, path := testStore(t)
	sub := checkStoreAdd(t, store)
	checkStoreListGet(t, store, &sub)
	checkStoreValidation(t)
	checkStorePersistence(t, path, &sub)
	checkStoreRemove(t, store, sub.ID)
}

func checkStoreAdd(t *testing.T, store *Store) Subscription {
	t.Helper()
	sub, created, err := store.Add(&Subscription{
		Provider: "generic", URL: "https://hooks.example.test/alert",
		Events: []string{"message.created", "message.updated"}, SecretRef: "webhook_secret",
	})
	if err != nil || !created {
		t.Fatalf("Add = %+v created=%t err=%v", sub, created, err)
	}
	if sub.ID == "" || sub.CreatedAt.IsZero() || sub.UpdatedAt.IsZero() {
		t.Fatalf("Add did not stamp the row: %+v", sub)
	}
	again, created, err := store.Add(&Subscription{
		Provider: "generic", URL: "https://hooks.example.test/alert",
		Events: []string{"message.created", "message.updated"}, SecretRef: "webhook_secret",
	})
	if err != nil || created || again.ID != sub.ID {
		t.Fatalf("idempotent Add = %+v created=%t err=%v", again, created, err)
	}
	return sub
}

func checkStoreListGet(t *testing.T, store *Store, sub *Subscription) {
	t.Helper()
	rows := store.List()
	if len(rows) != 1 || rows[0].ID != sub.ID {
		t.Fatalf("List = %+v", rows)
	}
	rows[0].Events[0] = "mutated"
	if store.List()[0].Events[0] != "message.created" {
		t.Fatal("List returned the internal slice")
	}
	got, ok := store.Get(sub.ID)
	if !ok || got.URL != sub.URL || !slices.Equal(got.Events, sub.Events) {
		t.Fatalf("Get = %+v ok=%t", got, ok)
	}
	if _, ok := store.Get("wh_missing"); ok {
		t.Fatal("Get accepted an unknown id")
	}
}

func checkStoreValidation(t *testing.T) {
	t.Helper()
	store, _ := testStore(t)
	now := time.Now().UTC()
	good := Subscription{URL: "https://hooks.example.test/x", Events: []string{"e"}, SecretRef: "webhook_secret"}
	cases := map[string]Subscription{
		"plain public http": {URL: "http://hooks.example.test/x", Events: []string{"e"}, SecretRef: "s"},
		"credentials":       {URL: "https://user:pw@hooks.example.test/x", Events: []string{"e"}, SecretRef: "s"}, // #nosec G101 -- fixture, not a credential.
		"no host":           {URL: "https:///x", Events: []string{"e"}, SecretRef: "s"},
		"unknown provider":  {Provider: "carrier-pigeon", URL: good.URL, Events: []string{"e"}, SecretRef: "s"},
		"no events":         {URL: good.URL, SecretRef: "s"},
		"bad event":         {URL: good.URL, Events: []string{"Bad Event"}, SecretRef: "s"},
		"bad secret ref":    {URL: good.URL, Events: []string{"e"}, SecretRef: "not a name"},
	}
	for name := range cases {
		sub := cases[name]
		if _, _, err := store.Add(&sub); !errors.Is(err, ErrSubscription) {
			t.Fatalf("%s = %v, want ErrSubscription", name, err)
		}
	}
	loopback := good
	loopback.URL = "http://127.0.0.1:8080/hook"
	loopback.ExpiresAt = now
	if _, _, err := store.Add(&loopback); err != nil {
		t.Fatalf("loopback http refused: %v", err)
	}
	msgraph := good
	msgraph.Provider, msgraph.URL = "msgraph", "https://hooks.example.test/graph"
	if _, _, err := store.Add(&msgraph); err != nil {
		t.Fatalf("msgraph target refused: %v", err)
	}
	if err := ValidateSubscription(nil); !errors.Is(err, ErrSubscription) {
		t.Fatalf("nil = %v", err)
	}
}

func checkStorePersistence(t *testing.T, path string, sub *Subscription) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); runtime.GOOS != "windows" && perm != 0o600 {
		t.Fatalf("store mode = %o, want 600", perm)
	}
	reopened, err := OpenStore(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := reopened.Get(sub.ID)
	if !ok || got.URL != sub.URL || got.Provider != "generic" {
		t.Fatalf("reopened row = %+v ok=%t", got, ok)
	}
	if _, err := OpenStore(filepath.Join(t.TempDir(), "missing.json"), nil); err != nil {
		t.Fatalf("open missing = %v", err)
	}
	if _, err := OpenStore("", nil); !errors.Is(err, ErrSubscription) {
		t.Fatalf("empty path = %v", err)
	}
	badPath := filepath.Join(t.TempDir(), "bad.json")
	if werr := os.WriteFile(badPath, []byte("{"), 0o600); werr != nil {
		t.Fatal(werr)
	}
	if _, err := OpenStore(badPath, nil); err == nil {
		t.Fatal("corrupt store was accepted")
	}
}

func checkStoreRemove(t *testing.T, store *Store, id string) {
	t.Helper()
	if err := store.Remove(id); err != nil {
		t.Fatal(err)
	}
	if len(store.List()) != 0 {
		t.Fatalf("List after Remove = %+v", store.List())
	}
	if err := store.Remove(id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Remove = %v, want ErrNotFound", err)
	}
}

// TestF7WebhookStoreExpiry: renewal moves one row's expiry, and Due returns
// only rows inside the lead window, earliest first.
func TestF7WebhookStoreExpiry(t *testing.T) {
	t.Parallel()
	store, _ := testStore(t)
	now := time.Unix(1_700_000_000, 0)
	first := Subscription{
		ID: "wh_1", Provider: "msgraph", URL: "https://hooks.example.test/a",
		Events: []string{"e"}, SecretRef: "s", ExpiresAt: now.Add(5 * time.Minute),
	}
	second := Subscription{
		ID: "wh_2", Provider: "msgraph", URL: "https://hooks.example.test/b",
		Events: []string{"e"}, SecretRef: "s", ExpiresAt: now.Add(10 * time.Minute),
	}
	never := Subscription{
		ID: "wh_3", Provider: "generic", URL: "https://hooks.example.test/c",
		Events: []string{"e"}, SecretRef: "s",
	}
	for _, sub := range []*Subscription{&first, &second, &never} {
		if _, _, err := store.Add(sub); err != nil {
			t.Fatal(err)
		}
	}
	due := store.Due(now, 15*time.Minute)
	if len(due) != 2 || due[0].ID != "wh_1" || due[1].ID != "wh_2" {
		t.Fatalf("Due = %+v", due)
	}
	if got := store.Due(now, time.Minute); len(got) != 0 {
		t.Fatalf("Due outside lead = %+v", got)
	}
	extended := now.Add(2 * time.Hour)
	if err := store.SetExpiry("wh_1", extended); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get("wh_1")
	if !got.ExpiresAt.Equal(extended) {
		t.Fatalf("expiry = %s, want %s", got.ExpiresAt, extended)
	}
	if err := store.SetExpiry("wh_missing", extended); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown SetExpiry = %v, want ErrNotFound", err)
	}
}
