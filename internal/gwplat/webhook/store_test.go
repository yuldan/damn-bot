package webhook

import (
	"errors"
	"path/filepath"
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

// TestF7WebhookStore: dynamic subscriptions are persistent, idempotent,
// validated (https or loopback), removable, and expiring rows are reported for
// renewal.
func TestF7WebhookStore(t *testing.T) {
	t.Parallel()
	store, path := testStore(t)
	now := time.Unix(1_700_000_000, 0)
	store.now = func() time.Time { return now }
	sub, created, err := store.Add(Subscription{
		Provider: "generic", URL: "https://hooks.example.test/alert",
		Events: []string{"message.created"}, SecretRef: "webhook_secret",
	})
	if err != nil || !created || sub.ID == "" {
		t.Fatalf("Add = %+v created=%t err=%v", sub, created, err)
	}
	same, created, err := store.Add(Subscription{
		Provider: "generic", URL: "https://hooks.example.test/alert",
		Events: []string{"message.created"}, SecretRef: "webhook_secret",
	})
	if err != nil || created || same.ID != sub.ID {
		t.Fatalf("idempotent Add = %+v created=%t err=%v", same, created, err)
	}
	reopened, err := OpenStore(path, nil)
	if err != nil || len(reopened.List()) != 1 {
		t.Fatalf("reopen = %d rows, %v", len(reopened.List()), err)
	}
	for _, bad := range []Subscription{
		{URL: "http://hooks.example.test/x", Events: []string{"message.created"}, SecretRef: "s"},
		{URL: "https://user:pass@hooks.example.test/x", Events: []string{"message.created"}, SecretRef: "s"},
		{URL: "https://hooks.example.test/x", Events: nil, SecretRef: "s"},
		{URL: "https://hooks.example.test/x", Events: []string{"BAD EVENT"}, SecretRef: "s"},
		{URL: "https://hooks.example.test/x", Events: []string{"message.created"}, SecretRef: ""},
		{Provider: "slack", URL: "https://hooks.example.test/x", Events: []string{"message.created"}, SecretRef: "s"},
	} {
		if _, _, berr := store.Add(bad); !errors.Is(berr, ErrSubscription) {
			t.Fatalf("invalid subscription accepted: %+v (%v)", bad, berr)
		}
	}
	if _, _, err := store.Add(Subscription{
		URL:    "http://127.0.0.1:9/hook",
		Events: []string{"message.created"}, SecretRef: "webhook_secret",
	}); err != nil {
		t.Fatalf("loopback http rejected: %v", err)
	}

	expiring, _, err := store.Add(Subscription{
		Provider: "msgraph", URL: "https://hooks.example.test/graph",
		Events: []string{"message.created"}, SecretRef: "msgraph_client_state",
		ExpiresAt: now.Add(10 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if due := store.Due(now, 15*time.Minute); len(due) != 1 || due[0].ID != expiring.ID {
		t.Fatalf("due = %+v", due)
	}
	if due := store.Due(now, time.Minute); len(due) != 0 {
		t.Fatalf("early due = %+v", due)
	}
	if err := store.Remove(sub.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Get(sub.ID); ok {
		t.Fatal("removed subscription still visible")
	}
	if err := store.Remove("wh_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("remove unknown = %v", err)
	}
}
