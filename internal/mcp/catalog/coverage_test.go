package catalog

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type errSeq struct {
	last    int64
	lastErr error
	saveErr error
}

func (s errSeq) Last(Kind, string) (int64, error) { return s.last, s.lastErr }
func (s errSeq) Save(Kind, string, int64) error   { return s.saveErr }

type errDoer struct{ err error }

func (d errDoer) Do(*http.Request) (*http.Response, error) { return nil, d.err }

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (errReader) Close() error             { return nil }

type bodyDoer struct{ resp *http.Response }

func (d bodyDoer) Do(*http.Request) (*http.Response, error) { return d.resp, nil }

func TestGAPF5_SEC20VerifyWithStoreFailures(t *testing.T) {
	t.Parallel()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	m := testManifest(KindMCP, 3)
	raw := signedManifest(t, priv, &m)
	if _, err := VerifyWithStore(raw, pub, nil); !errors.Is(err, ErrMalformed) {
		t.Fatalf("nil store = %v", err)
	}
	if _, err := VerifyWithStore(raw, pub, errSeq{lastErr: errors.New("disk")}); err == nil || !strings.Contains(err.Error(), "read serial") {
		t.Fatalf("last error = %v", err)
	}
	if _, err := VerifyWithStore(raw, pub, errSeq{saveErr: errors.New("disk")}); err == nil || !strings.Contains(err.Error(), "save serial") {
		t.Fatalf("save error = %v", err)
	}
	if _, err := VerifyWithStore([]byte("{"), pub, errSeq{}); err == nil {
		t.Fatal("malformed accepted")
	}
}

func TestGAPF5_SEC20SeqStoreKeyAndDirFailures(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, tc := range []struct {
		store FileSeqStore
		kind  Kind
		name  string
	}{
		{FileSeqStore{}, KindMCP, "demo"},
		{FileSeqStore{Dir: dir}, "", "demo"},
		{FileSeqStore{Dir: dir}, KindMCP, ""},
		{FileSeqStore{Dir: dir}, KindMCP, "../demo"},
		{FileSeqStore{Dir: dir}, KindMCP, `a\b`},
		{FileSeqStore{Dir: dir}, KindMCP, "."},
		{FileSeqStore{Dir: dir}, KindMCP, ".."},
	} {
		if _, err := tc.store.Last(tc.kind, tc.name); !errors.Is(err, ErrMalformed) {
			t.Fatalf("Last(%q,%q) = %v", tc.kind, tc.name, err)
		}
		if err := tc.store.Save(tc.kind, tc.name, 1); !errors.Is(err, ErrMalformed) {
			t.Fatalf("Save(%q,%q) = %v", tc.kind, tc.name, err)
		}
	}
	file := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (FileSeqStore{Dir: file}).Last(KindMCP, "demo"); err == nil {
		t.Fatal("Last with file dir accepted")
	}
	if err := (FileSeqStore{Dir: file}).Save(KindMCP, "demo", 1); err == nil {
		t.Fatal("Save with file dir accepted")
	}
}

func TestGAPF5_SEC20FetchFailures(t *testing.T) {
	t.Parallel()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	m := testManifest(KindMCP, 4)
	raw := signedManifest(t, priv, &m)
	if _, ferr := Fetch(context.Background(), nil, "https://example.test/c", pub, 0); !errors.Is(ferr, ErrMalformed) {
		t.Fatalf("nil client = %v", ferr)
	}
	if _, ferr := Fetch(context.Background(), errDoer{}, "", pub, 0); !errors.Is(ferr, ErrMalformed) {
		t.Fatalf("empty URL = %v", ferr)
	}
	if _, ferr := Fetch(context.Background(), errDoer{}, "://bad", pub, 0); !errors.Is(ferr, ErrMalformed) {
		t.Fatalf("bad URL = %v", ferr)
	}
	if _, ferr := Fetch(context.Background(), errDoer{err: errors.New("dial")}, "https://example.test/c", pub, 0); ferr == nil || !strings.Contains(ferr.Error(), "fetch") {
		t.Fatalf("do error = %v", ferr)
	}
	if _, ferr := Fetch(context.Background(), bodyDoer{resp: &http.Response{StatusCode: 500, Body: http.NoBody}}, "https://example.test/c", pub, 0); ferr == nil || !strings.Contains(ferr.Error(), "status 500") {
		t.Fatalf("status = %v", ferr)
	}
	if _, ferr := Fetch(context.Background(), bodyDoer{resp: &http.Response{StatusCode: 200, Body: errReader{}}}, "https://example.test/c", pub, 0); ferr == nil || !strings.Contains(ferr.Error(), "read manifest") {
		t.Fatalf("read error = %v", ferr)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, werr := w.Write(raw); werr != nil {
			return
		}
	}))
	t.Cleanup(srv.Close)
	got, err := Fetch(context.Background(), srv.Client(), srv.URL, pub, 3)
	if err != nil || got.Serial != 4 {
		t.Fatalf("valid fetch = %+v, %v", got, err)
	}
}

func TestGAPF5_SEC20ManifestValidation(t *testing.T) {
	t.Parallel()
	base := testManifest(KindMCP, 1)
	cases := []struct {
		name string
		mod  func(*Manifest)
	}{
		{"kind", func(m *Manifest) { m.Kind = "other" }},
		{"name", func(m *Manifest) { m.Name = "" }},
		{"version", func(m *Manifest) { m.Version = "" }},
		{"serial", func(m *Manifest) { m.Serial = 0 }},
		{"no entries", func(m *Manifest) { m.Entries = nil }},
		{"id empty", func(m *Manifest) { m.Entries[0].ID = "" }},
		{"id long", func(m *Manifest) { m.Entries[0].ID = strings.Repeat("x", 129) }},
		{"commit length", func(m *Manifest) { m.Entries[0].Commit = "abc" }},
		{"commit hex", func(m *Manifest) { m.Entries[0].Commit = strings.Repeat("z", 40) }},
		{"sha length", func(m *Manifest) { m.Entries[0].SHA256 = "abc" }},
		{"sha hex", func(m *Manifest) { m.Entries[0].SHA256 = strings.Repeat("z", 64) }},
		{"too many", func(m *Manifest) {
			m.Entries = make([]Entry, 4097)
			for i := range m.Entries {
				m.Entries[i] = base.Entries[0]
			}
		}},
	}
	for _, tc := range cases {
		m := base
		m.Entries = append([]Entry(nil), base.Entries...)
		tc.mod(&m)
		if _, err := ParseManifest(mustJSON(t, m)); err == nil {
			t.Fatalf("%s accepted", tc.name)
		}
	}
	if _, err := ParseManifest([]byte(`{"kind":"mcp","name":"x","version":"1","serial":1,"entries":[],"extra":true}`)); !errors.Is(err, ErrMalformed) {
		t.Fatalf("unknown field = %v", err)
	}
	if _, err := ParseManifest(append(mustJSON(t, base), []byte("{}")...)); !errors.Is(err, ErrMalformed) {
		t.Fatalf("trailing data = %v", err)
	}
}
