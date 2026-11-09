package hub

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"net/url"
	"slices"
	"testing"
)

// TestF8SKILL011Hub proves a source index is only usable with a trusted
// ed25519 key: no key and no signature both fail closed, a valid signature
// works, and the embedded catalog is not a fallback.
func TestF8SKILL011Hub(t *testing.T) {
	t.Parallel()
	body := skillBody
	fetch := mapFetcher{
		"https://skills.nousresearch.com/index.json":                   indexDoc(t, entryMap("findmy", body, len(body))),
		"https://skills.nousresearch.com/skills/apple/findmy/SKILL.md": body,
	}
	svc := newTestService(t, &fakeStore{})
	unsigned, err := New(&Config{Fetcher: fetch, Service: svc})
	if err != nil {
		t.Fatalf("New without key: %v", err)
	}
	if _, ierr := unsigned.Index(t.Context(), SourceOfficial); !errors.Is(ierr, ErrSignature) {
		t.Fatalf("Index without key = %v, want ErrSignature", ierr)
	}
	h := newTestHub(t, fetch, &fakeStore{}, &Config{})
	idx, err := h.Index(t.Context(), SourceOfficial)
	if err != nil || len(idx.Entries) != 1 {
		t.Fatalf("signed index = %+v, %v", idx, err)
	}
	if _, err := h.Download(t.Context(), SourceOfficial, &idx.Entries[0]); err != nil {
		t.Fatalf("signed Download: %v", err)
	}
}

// TestCatalogSignature is the negative control of the index signature:
// tampered bytes, missing signature file, malformed hex, and a wrong trusted
// key are all rejected.
func TestCatalogSignature(t *testing.T) {
	t.Parallel()
	body := skillBody
	index := indexDoc(t, entryMap("findmy", body, len(body)))
	sig := hex.EncodeToString(ed25519.Sign(testKey(), []byte(index)))
	cases := []struct {
		name string
		get  func(context.Context, *url.URL, int64) ([]byte, error)
		key  ed25519.PublicKey
	}{
		{"tampered", fetcherFunc(func(_ context.Context, u *url.URL, _ int64) ([]byte, error) {
			if u.Path == "/index.json.sig" {
				return []byte(sig), nil
			}
			return []byte(index + " "), nil
		}), testPublicKey()},
		{"missing sig", fetcherFunc(func(_ context.Context, u *url.URL, _ int64) ([]byte, error) {
			if u.Path == "/index.json.sig" {
				return nil, ErrStatus
			}
			return []byte(index), nil
		}), testPublicKey()},
		{"malformed", fetcherFunc(func(_ context.Context, u *url.URL, _ int64) ([]byte, error) {
			if u.Path == "/index.json.sig" {
				return []byte("not-hex"), nil
			}
			return []byte(index), nil
		}), testPublicKey()},
		{"wrong key", fetcherFunc(func(_ context.Context, u *url.URL, _ int64) ([]byte, error) {
			if u.Path == "/index.json.sig" {
				return []byte(sig), nil
			}
			return []byte(index), nil
		}), ed25519.PublicKey(slices.Clone(ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))[ed25519.SeedSize:]))},
	}
	for _, tc := range cases {
		svc := newTestService(t, &fakeStore{})
		h, err := New(&Config{Fetcher: fetcherFunc(tc.get), Service: svc, IndexKey: tc.key})
		if err != nil {
			t.Fatalf("%s: New: %v", tc.name, err)
		}
		if _, ierr := h.Index(t.Context(), SourceOfficial); !errors.Is(ierr, ErrSignature) {
			t.Errorf("%s: Index = %v, want ErrSignature", tc.name, ierr)
		}
	}
}
