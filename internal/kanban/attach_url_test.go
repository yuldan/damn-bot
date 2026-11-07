package kanban

import (
	"context"
	"errors"
	"net"
	"testing"
)

type countingFetcher struct{ calls int }

func (f *countingFetcher) Get(context.Context, string, int64) ([]byte, error) {
	f.calls++
	return []byte("body"), nil
}

// TestF7KanbanAttachURLGateRejectsBeforeFetch proves the SSRF gate runs before
// any transport: scheme, credentials, localhost, metadata and every private
// resolved address are refused with zero fetch calls.
func TestF7KanbanAttachURLGateRejectsBeforeFetch(t *testing.T) {
	t.Parallel()
	store := New(nil, t.TempDir())
	fetch := &countingFetcher{}
	store.Fetch = fetch
	store.Resolve = func(string) ([]net.IP, error) { return []net.IP{net.ParseIP("10.1.2.3")}, nil }
	for _, raw := range []string{
		"http://public.example/a",
		"https://user:pw@public.example/a",
		"https://localhost/a",
		"https://metadata.google.internal/a",
		"https://127.0.0.1/a",
		"https://internal.example/a",
	} {
		if _, err := store.AttachURL(t.Context(), "b-t/a", raw, "u"); !errors.Is(err, ErrBlockedURL) {
			t.Errorf("%s err = %v, want ErrBlockedURL", raw, err)
		}
	}
	if fetch.calls != 0 {
		t.Fatalf("fetch called %d times before the gate", fetch.calls)
	}
}
