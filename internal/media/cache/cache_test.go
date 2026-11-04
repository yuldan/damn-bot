package cache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/png"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type memStore struct {
	mu sync.Mutex
	m  map[string][]byte
}

func newMemStore() *memStore { return &memStore{m: map[string][]byte{}} }

func (s *memStore) Get(_ context.Context, key []byte) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[string(key)]
	return append([]byte(nil), v...), ok, nil
}

func (s *memStore) Put(_ context.Context, key, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[string(key)] = append([]byte(nil), value...)
	return nil
}

func (s *memStore) Delete(_ context.Context, key []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, string(key))
	return nil
}

func (s *memStore) List(_ context.Context, prefix []byte) ([][]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out [][]byte
	for k := range s.m {
		if strings.HasPrefix(k, string(prefix)) {
			out = append(out, []byte(k))
		}
	}
	return out, nil
}

func (s *memStore) keys(prefix string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for k := range s.m {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out
}

func pngBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	for i := range img.Pix {
		img.Pix[i] = 7
	}
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 255
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func newCache(t *testing.T) (*Store, *memStore) {
	t.Helper()
	kv := newMemStore()
	s, err := New(kv)
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Unix(1_700_000_000, 0) }
	return s, kv
}

// TestF6MEDIA030MediaCacheRepair covers the ledger row: content+parameter
// keys, blob dedupe, MIME sniff, idempotent repair, and a SHA mismatch that
// is a miss rather than corrupt data.
func TestF6MEDIA030MediaCacheRepair(t *testing.T) {
	t.Parallel()
	t.Run("roundtrip", testCacheRoundTrip)
	t.Run("corrupt repair", testCacheCorruption)
	t.Run("mime and documents", testCacheMIME)
}

func testCacheRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, kv := newCache(t)
	img := pngBytes(t)
	params := map[string]string{"prompt": "describe", "model": "fake/v"}
	entry, err := s.Put(ctx, PutRequest{Kind: "image", MIME: "image/png", Data: img, Params: params})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if entry.SHA == "" || entry.MIME != "image/png" {
		t.Fatalf("entry = %+v", entry)
	}
	got, gotEntry, ok, gerr := s.Lookup(ctx, LookupRequest{Kind: "image", MIME: "image/png", Data: img, Params: params})
	if gerr != nil || !ok || !bytes.Equal(got, img) || gotEntry.Key != entry.Key {
		t.Fatalf("lookup = %d bytes, %+v, %v, %v", len(got), gotEntry, ok, gerr)
	}
	other, _, kerr := Key("image", "image/png", img, map[string]string{"prompt": "other"})
	if kerr != nil || other == entry.Key {
		t.Fatalf("parameter key = %s, %v", other, kerr)
	}
	if _, perr := s.Put(ctx, PutRequest{
		Kind: "image", MIME: "image/png", Data: img,
		Params: map[string]string{"prompt": "other"},
	}); perr != nil {
		t.Fatal(perr)
	}
	if blobs := kv.keys("blob/"); len(blobs) != 1 {
		t.Fatalf("blobs = %v, want 1", blobs)
	}
}

func testCacheCorruption(t *testing.T) {
	t.Run("blob", testCorruptBlob)
	t.Run("meta", testCorruptMeta)
	t.Run("repair", testRepair)
}

func testCorruptBlob(t *testing.T) {
	ctx := context.Background()
	s, kv := newCache(t)
	img := pngBytes(t)
	entry, err := s.Put(ctx, PutRequest{Kind: "image", MIME: "image/png", Data: img})
	if err != nil {
		t.Fatal(err)
	}
	blobKey := "blob/" + entry.SHA
	bad := append([]byte(nil), img...)
	bad[20] ^= 0xFF
	if perr := kv.Put(ctx, []byte(blobKey), bad); perr != nil {
		t.Fatal(perr)
	}
	if _, _, ok, gerr := s.Get(ctx, entry.Key); gerr != nil || ok {
		t.Fatalf("corrupt blob = ok:%v err:%v", ok, gerr)
	}
	if _, ok, gerr := kv.Get(ctx, []byte(blobKey)); gerr != nil || ok {
		t.Fatalf("corrupt blob survived: ok=%v err=%v", ok, gerr)
	}
	if _, _, ok, gerr := s.Get(ctx, entry.Key); gerr != nil || ok {
		t.Fatalf("repaired entry served twice: ok=%v err=%v", ok, gerr)
	}
}

func testCorruptMeta(t *testing.T) {
	ctx := context.Background()
	s, kv := newCache(t)
	key := "entry/deadbeef"
	if err := kv.Put(ctx, []byte(key), []byte("{not json")); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, gerr := s.Get(ctx, "deadbeef"); gerr != nil || ok {
		t.Fatalf("corrupt meta served: ok=%v err=%v", ok, gerr)
	}
	if _, ok, gerr := kv.Get(ctx, []byte(key)); gerr != nil || ok {
		t.Fatalf("corrupt meta survived: ok=%v err=%v", ok, gerr)
	}
}

func testRepair(t *testing.T) {
	ctx := context.Background()
	s, kv := newCache(t)
	if err := kv.Put(ctx, []byte("entry/corrupt"), []byte(`{"kind":"image","sha":"deadbeef","bytes":3}`)); err != nil {
		t.Fatal(err)
	}
	orphan := sha256.Sum256([]byte("orphan"))
	if err := kv.Put(ctx, []byte("blob/"+hex.EncodeToString(orphan[:])), []byte("orphan")); err != nil {
		t.Fatal(err)
	}
	removed, err := s.Repair(ctx)
	if err != nil || removed < 2 {
		t.Fatalf("repair = %d, %v", removed, err)
	}
	if again, rerr := s.Repair(ctx); rerr != nil || again != 0 {
		t.Fatalf("repair not idempotent = %d, %v", again, rerr)
	}
}

func testCacheMIME(t *testing.T) {
	ctx := context.Background()
	s, _ := newCache(t)
	img := pngBytes(t)
	if _, err := s.Put(ctx, PutRequest{Kind: "image", MIME: "image/jpeg", Data: img}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mismatched MIME = %v", err)
	}
	pdf := append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte("obj"), 10)...)
	de, err := s.Put(ctx, PutRequest{Kind: "document", MIME: "application/pdf", Data: pdf})
	if err != nil {
		t.Fatalf("document put: %v", err)
	}
	doc, _, ok, gerr := s.Get(ctx, de.Key)
	if gerr != nil || !ok || !bytes.Equal(doc, pdf) {
		t.Fatalf("document roundtrip = ok:%v err:%v", ok, gerr)
	}
	if _, perr := s.Put(ctx, PutRequest{Kind: "nope", Data: img}); !errors.Is(perr, ErrInvalid) {
		t.Fatalf("unknown kind = %v", perr)
	}
}

// TestF6MediaCacheEviction: eviction is deterministic and drops the oldest
// entries (tie broken by key) down to the target.
func TestF6MediaCacheEviction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, kv := newCache(t)
	var first string
	for i := range MaxEntries + 6 {
		data := pngBytes(t)
		entry, err := s.Put(ctx, PutRequest{
			Kind: "image", MIME: "image/png", Data: data,
			Params: map[string]string{"n": strconv.Itoa(i)},
		})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = entry.Key
		}
	}
	entries := kv.keys("entry/")
	if len(entries) >= MaxEntries {
		t.Fatalf("entries after eviction = %d, want below %d", len(entries), MaxEntries)
	}
	for _, key := range entries {
		if key == "entry/"+first {
			t.Fatal("oldest entry survived eviction")
		}
	}
	// Repeating the same insertion order on a fresh store yields the same set.
	s2, kv2 := newCache(t)
	for i := range MaxEntries + 6 {
		data := pngBytes(t)
		if _, err := s2.Put(ctx, PutRequest{
			Kind: "image", MIME: "image/png", Data: data,
			Params: map[string]string{"n": strconv.Itoa(i)},
		}); err != nil {
			t.Fatal(err)
		}
	}
	got := kv.keys("entry/")
	want := kv2.keys("entry/")
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("determinism: %d vs %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("determinism at %d: %s vs %s", i, got[i], want[i])
		}
	}
}

// TestF6MediaCacheOversizeAndSeq: a blob over the cap is rejected, and a
// missing entry is a clean miss.
func TestF6MediaCacheOversizeAndSeq(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newCache(t)
	if _, err := s.Put(ctx, PutRequest{Kind: "text", MIME: "text/plain", Data: make([]byte, MaxEntryBytes+1)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversize = %v", err)
	}
	if _, _, ok, err := s.Get(ctx, "missing"); err != nil || ok {
		t.Fatalf("missing = ok:%v err:%v", ok, err)
	}
}
