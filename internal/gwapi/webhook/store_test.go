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
	// Windows reports 0666 for every writable file: Mode().Perm() only mirrors
	// FILE_ATTRIBUTE_READONLY (os/types_windows.go), so chmod(0600) cannot
	// round-trip there. The production path still chmods 0600 before the
	// rename; this assertion is meaningful only on platforms with POSIX bits.
	if perm := statPerm(t, path); posixModes() && perm != 0o600 {
		t.Fatalf("store mode = %o, want 600", perm)
	}
	if perm := statPerm(t, filepath.Dir(path)); posixModes() && perm != 0o700 {
		t.Fatalf("store dir mode = %o, want 700", perm)
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

// posixModes reports whether the platform keeps POSIX permission bits. Windows
// only keeps FILE_ATTRIBUTE_READONLY, so os.Stat reports 0666 for any writable
// file and 0444 for a read-only one; chmod(0600) is not observable there.
func posixModes() bool { return runtime.GOOS != "windows" }

func statPerm(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

// TestF7WebhookStoreRepairsLooseTable: a table and directory left with
// group/other access by an older release are repaired to 0600/0700 on open,
// and the existing rows are kept rather than rejected.
func TestF7WebhookStoreRepairsLooseTable(t *testing.T) {
	t.Parallel()
	if !posixModes() {
		t.Skip("POSIX permission bits")
	}
	store, path := testStore(t)
	sub, _, err := store.Add(&Subscription{
		Provider: "generic", URL: "https://hooks.example.test/legacy",
		Events: []string{"message.created"}, SecretRef: "webhook_secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(path)
	if cerr := os.Chmod(path, 0o644); cerr != nil { // #nosec G302 -- repair fixture.
		t.Fatal(cerr)
	}
	if cerr := os.Chmod(dir, 0o755); cerr != nil { // #nosec G302 -- repair fixture.
		t.Fatal(cerr)
	}
	reopened, err := OpenStore(path, nil)
	if err != nil {
		t.Fatalf("OpenStore on a loose table: %v", err)
	}
	if _, ok := reopened.Get(sub.ID); !ok {
		t.Fatal("loose table lost its rows")
	}
	if perm := statPerm(t, path); perm != 0o600 {
		t.Fatalf("repaired store mode = %o, want 600", perm)
	}
	if perm := statPerm(t, dir); perm != 0o700 {
		t.Fatalf("repaired store dir mode = %o, want 700", perm)
	}
}

// TestF7WebhookStoreRepairsLooseDirOnSave: a fresh table written into a
// directory that already exists with group/other access still ends up 0600
// inside a 0700 directory.
func TestF7WebhookStoreRepairsLooseDirOnSave(t *testing.T) {
	t.Parallel()
	if !posixModes() {
		t.Skip("POSIX permission bits")
	}
	dir := filepath.Join(t.TempDir(), "webhook")
	if err := os.MkdirAll(dir, 0o755); err != nil { // #nosec G301 -- repair fixture.
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil { // #nosec G302 -- repair fixture.
		t.Fatal(err)
	}
	path := filepath.Join(dir, "subscriptions.json")
	store, err := OpenStore(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Add(&Subscription{
		Provider: "generic", URL: "https://hooks.example.test/dir",
		Events: []string{"message.created"}, SecretRef: "webhook_secret",
	}); err != nil {
		t.Fatal(err)
	}
	if perm := statPerm(t, dir); perm != 0o700 {
		t.Fatalf("store dir mode = %o, want 700", perm)
	}
	if perm := statPerm(t, path); perm != 0o600 {
		t.Fatalf("store mode = %o, want 600", perm)
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
