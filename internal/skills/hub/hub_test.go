package hub

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/damnbot/damnbot/internal/skills"
)

const skillBody = "---\nname: findmy\ndescription: Find devices.\n---\n# Find my\n\nBody text.\n"

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func entryMap(name, body string, size int) map[string]any {
	sum := sha256.Sum256([]byte(body))
	return map[string]any{
		"name": name, "version": "1.0.0", "sha256": hex.EncodeToString(sum[:]),
		"path": "skills/apple/" + name + "/SKILL.md", "description": "Find devices.",
		"size": size, "tos_risk": "none", "tos_note": "official test entry",
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}

func writeBody(w http.ResponseWriter, body string) {
	_, _ = w.Write([]byte(body)) //nolint:errcheck // test handler
}

func indexDoc(t *testing.T, entries ...map[string]any) string {
	t.Helper()
	return mustJSON(t, map[string]any{"schema": 1, "entries": entries})
}

type mapFetcher map[string]string

func testKey() ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize))
}

func testPublicKey() ed25519.PublicKey {
	return ed25519.PublicKey(slices.Clone(testKey()[ed25519.SeedSize:]))
}

func (m mapFetcher) Get(_ context.Context, u *url.URL, limit int64) ([]byte, error) {
	if strings.HasSuffix(u.Path, ".sig") {
		idx, ok := m[strings.TrimSuffix(u.String(), ".sig")]
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrStatus, u.Redacted())
		}
		sig := hex.EncodeToString(ed25519.Sign(testKey(), []byte(idx)))
		return []byte(sig), nil
	}
	body, ok := m[u.String()]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrStatus, u.Redacted())
	}
	if int64(len(body)) > limit {
		return nil, ErrTooLarge
	}
	return []byte(body), nil
}

type fakeStore struct {
	next        int64
	indexed     []skills.Skill
	quarantined []skills.Skill
	scan        string
	approved    bool
	status      skills.Status
}

func (f *fakeStore) Index(_ context.Context, s *skills.Skill) (int64, error) {
	f.next++
	s.ID = f.next
	f.indexed = append(f.indexed, *s)
	f.approved = true
	return s.ID, nil
}

func (f *fakeStore) Quarantine(_ context.Context, s *skills.Skill, scan string, rejected bool) (int64, int64, error) {
	f.next++
	s.ID = f.next
	f.quarantined = append(f.quarantined, *s)
	f.scan = scan
	if rejected {
		f.status = skills.StatusRejected
	} else {
		f.status = skills.StatusScanned
	}
	return 7, s.ID, nil
}

func (f *fakeStore) Approve(_ context.Context, memoryID int64) error {
	if memoryID != 7 {
		return errors.New("wrong memory id")
	}
	f.approved, f.status = true, skills.StatusApproved
	return nil
}

func (f *fakeStore) Search(_ context.Context, _ string, k int) ([]skills.Skill, error) {
	if !f.approved {
		return nil, nil
	}
	return f.quarantined[:min(k, len(f.quarantined))], nil
}

func (f *fakeStore) Status(_ context.Context, _ int64) (skills.Status, error) { return f.status, nil }

// testScan is the minimal deterministic scanner of the source/download slice;
// the production grep scanner and its named tests arrive with the quarantine
// workflow slice.
func testScan(sk *skills.Skill) (string, string, error) {
	text := sk.Description + "\n" + sk.Body
	sev := "low"
	var findings []map[string]string
	if strings.Contains(text, "os/exec") || strings.Contains(text, ".ssh/") {
		sev = "high"
		findings = append(findings, map[string]string{"category": "shell_exec", "severity": "high"})
	}
	if strings.Contains(text, "../") {
		if sev == "low" {
			sev = "medium"
		}
		findings = append(findings, map[string]string{"category": "path_traversal", "severity": "medium"})
	}
	b, err := json.Marshal(map[string]any{"severity": sev, "findings": findings})
	if err != nil {
		return "", "", fmt.Errorf("test scan: %w", err)
	}
	return sev, string(b), nil
}

func newTestService(t *testing.T, st skills.Store) *skills.Service {
	t.Helper()
	svc, err := skills.New(st, skills.Config{
		Scan:   testScan,
		Redact: func(v string) string { return v },
		Files:  func(*skills.Skill) (fs.FS, error) { return fstest.MapFS{}, nil },
	})
	if err != nil {
		t.Fatalf("skills.New: %v", err)
	}
	return svc
}

func newTestHub(t *testing.T, fetcher Fetcher, st skills.Store, cfg *Config) *Hub {
	t.Helper()
	cfg.Fetcher = fetcher
	cfg.Service = newTestService(t, st)
	if cfg.IndexKey == nil {
		cfg.IndexKey = testPublicKey()
	}
	h, err := New(cfg)
	if err != nil {
		t.Fatalf("hub.New: %v", err)
	}
	return h
}

// TestF8SkillHubSources proves the closed four-source registry and the https
// host allowlist, including the negative controls for overrides.
func TestF8SkillHubSources(t *testing.T) {
	t.Parallel()
	if got := Sources(); len(got) != 4 || got[0] != SourceOfficial || got[3] != SourceSkillsSh {
		t.Fatalf("Sources() = %v", got)
	}
	for _, src := range Sources() {
		base, ok := DefaultBaseURL(src)
		if !ok || !strings.HasPrefix(base, "https://") {
			t.Errorf("%s: base %q ok=%v", src, base, ok)
		}
		if _, err := ValidateURL(src, base+"/index.json"); err != nil {
			t.Errorf("%s: default URL rejected: %v", src, err)
		}
	}
	if _, err := ParseSource("nope"); !errors.Is(err, ErrSource) {
		t.Errorf("ParseSource(unknown) = %v", err)
	}
	// A default source URL is https + allowlisted; an http downgrade, a
	// loopback host, and a foreign host are all rejected.
	bad := []string{
		"http://skills.nousresearch.com/index.json",
		"https://127.0.0.1/index.json",
		"https://localhost/index.json",
		"https://evil.example/index.json",
		"https://user:pw@skills.nousresearch.com/index.json",
		"https://skills.nousresearch.com:8443/index.json",
		"https://skills.nousresearch.com/../index.json",
	}
	for _, raw := range bad {
		if _, err := ValidateURL(SourceOfficial, raw); !errors.Is(err, ErrURL) {
			t.Errorf("ValidateURL(%q) = %v, want ErrURL", raw, err)
		}
	}
	// A source override is still held to the allowlist, and the test-only
	// seam accepts loopback only.
	_, err := New(&Config{
		Fetcher: mapFetcher{}, Service: newTestService(t, &fakeStore{}),
		Sources: map[Source]string{SourceOfficial: "http://127.0.0.1:9999"},
	})
	if !errors.Is(err, ErrURL) {
		t.Errorf("http override = %v, want ErrURL", err)
	}
	if _, err := New(&Config{
		Fetcher: mapFetcher{}, Service: newTestService(t, &fakeStore{}),
		Sources: map[Source]string{SourceOfficial: "http://127.0.0.1:9999"}, AllowTestSources: true,
	}); err != nil {
		t.Errorf("loopback test override rejected: %v", err)
	}
	if _, err := New(&Config{
		Fetcher: mapFetcher{}, Service: newTestService(t, &fakeStore{}),
		Sources: map[Source]string{SourceOfficial: "http://example.com"}, AllowTestSources: true,
	}); !errors.Is(err, ErrURL) {
		t.Errorf("public test override = %v, want ErrURL", err)
	}
	if _, err := New(&Config{}); !errors.Is(err, ErrURL) {
		t.Errorf("nil deps = %v, want ErrURL", err)
	}
}

// TestHubIndexFetchesSource proves Index resolves one source's index and
// rejects a source outside the closed registry.
func TestHubIndexFetchesSource(t *testing.T) {
	t.Parallel()
	fetch := mapFetcher{
		"https://skills.nousresearch.com/index.json": indexDoc(t, entryMap("findmy", skillBody, len(skillBody))),
	}
	h := newTestHub(t, fetch, &fakeStore{}, &Config{})
	idx, err := h.Index(t.Context(), SourceOfficial)
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if idx.Source != SourceOfficial || len(idx.Entries) != 1 || idx.Entries[0].Name != "findmy" {
		t.Fatalf("Index = %+v", idx)
	}
	if _, err := h.Index(t.Context(), Source("nope")); !errors.Is(err, ErrSource) {
		t.Fatalf("unknown source = %v, want ErrSource", err)
	}
}

// TestF8SkillHubQuarantine proves a downloaded skill is stored as scanned,
// carries the threat scan result, and is not retrievable before approval.
func TestF8SkillHubQuarantine(t *testing.T) {
	t.Parallel()
	body := strings.Replace(skillBody, "Body text.", "Read ../outside for context.", 1)
	st := &fakeStore{}
	fetch := mapFetcher{
		"https://skills.nousresearch.com/index.json":                   indexDoc(t, entryMap("findmy", body, len(body))),
		"https://skills.nousresearch.com/skills/apple/findmy/SKILL.md": body,
	}
	h := newTestHub(t, fetch, st, &Config{})
	inst, err := h.Download(t.Context(), SourceOfficial, new(mustEntry(t, fetch)))
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if inst.Status != skills.StatusScanned {
		t.Fatalf("status = %q, want scanned (pending review)", inst.Status)
	}
	if len(st.quarantined) != 1 || st.approved {
		t.Fatalf("quarantined=%d approved=%v", len(st.quarantined), st.approved)
	}
	if !strings.Contains(st.scan, "path_traversal") {
		t.Errorf("scan result lost findings: %s", st.scan)
	}
	svc := newTestService(t, st)
	if got, rerr := svc.Retrieve(t.Context(), "findmy", 3); rerr != nil || len(got) != 0 {
		t.Fatalf("pending skill retrievable: %v %v", got, rerr)
	}
	// A high finding (shell execution) is rejected outright: it never even
	// reaches the review queue, and the store receives no indexed row.
	evil := strings.Replace(skillBody, "name: findmy", "name: evil", 1)
	evil = strings.Replace(evil, "Body text.", "Run os/exec.", 1)
	evilFetch := mapFetcher{
		"https://skills.nousresearch.com/index.json":                 indexDoc(t, entryMap("evil", evil, len(evil))),
		"https://skills.nousresearch.com/skills/apple/evil/SKILL.md": evil,
	}
	est := &fakeStore{}
	eh := newTestHub(t, evilFetch, est, &Config{})
	inst, err = eh.Download(t.Context(), SourceOfficial, new(mustEntry(t, evilFetch)))
	if err != nil {
		t.Fatalf("high-risk Download: %v", err)
	}
	if inst.Status != skills.StatusRejected || est.approved || len(est.indexed) != 0 {
		t.Fatalf("high finding: status=%q indexed=%d approved=%v", inst.Status, len(est.indexed), est.approved)
	}
}

// mustEntry derives the index entry for a body, so the test never hardcodes a
// digest.
func mustEntry(t *testing.T, fetch mapFetcher) Entry {
	t.Helper()
	raw, ok := fetch["https://skills.nousresearch.com/index.json"]
	if !ok {
		t.Fatal("missing index")
	}
	idx, err := ParseIndex(SourceOfficial, []byte(raw))
	if err != nil {
		t.Fatalf("ParseIndex: %v", err)
	}
	return idx.Entries[0]
}

// TestF8SkillHubRejectsHTTP covers the URL policy, cross-host redirect, and
// bounded/binary body rules.
func TestF8SkillHubRejectsHTTP(t *testing.T) {
	t.Parallel()
	far := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeBody(w, "far")
	}))
	defer far.Close()
	near := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/two":
			w.Header().Set("Content-Type", "text/plain")
			writeBody(w, "same host")
		case "/binary":
			_, _ = w.Write([]byte{'a', 0, 'b'}) //nolint:errcheck // test handler
		case "/big":
			_, _ = w.Write([]byte(strings.Repeat("x", 32))) //nolint:errcheck // test handler
		default:
			http.Redirect(w, r, far.URL+"/far", http.StatusFound)
		}
	}))
	defer near.Close()
	f, err := NewHTTPFetcher(near.Client())
	if err != nil {
		t.Fatalf("NewHTTPFetcher: %v", err)
	}
	u := mustURL(t, near.URL+"/one")
	if _, ferr := f.Get(t.Context(), u, 1024); !errors.Is(ferr, ErrRedirect) {
		t.Errorf("cross-host redirect = %v, want ErrRedirect", ferr)
	}
	same := mustURL(t, near.URL+"/two")
	if body, ferr := f.Get(t.Context(), same, 1024); ferr != nil || string(body) != "same host" {
		t.Errorf("same-host fetch = %q, %v", body, ferr)
	}
	bin := mustURL(t, near.URL+"/binary")
	if _, ferr := f.Get(t.Context(), bin, 1024); !errors.Is(ferr, ErrBinary) {
		t.Errorf("binary body = %v, want ErrBinary", ferr)
	}
	big := mustURL(t, near.URL+"/big")
	if _, ferr := f.Get(t.Context(), big, 8); !errors.Is(ferr, ErrTooLarge) {
		t.Errorf("oversized body = %v, want ErrTooLarge", ferr)
	}
	if _, nerr := NewHTTPFetcher(nil); !errors.Is(nerr, ErrURL) {
		t.Errorf("nil client = %v, want ErrURL", nerr)
	}
	plain, err := NewFetcher(near.Client())
	if err != nil {
		t.Fatalf("NewFetcher: %v", err)
	}
	if body, ferr := plain.Get(t.Context(), same, 1024); ferr != nil || string(body) != "same host" {
		t.Errorf("Doer fetch = %q, %v", body, ferr)
	}
	if _, nerr := NewFetcher(nil); !errors.Is(nerr, ErrURL) {
		t.Errorf("nil doer = %v, want ErrURL", nerr)
	}
}

// TestF8SkillHubHashMismatch proves a wrong pinned digest writes nothing.
func TestF8SkillHubHashMismatch(t *testing.T) {
	t.Parallel()
	body := skillBody
	e := new(mustEntry(t, mapFetcher{"https://skills.nousresearch.com/index.json": indexDoc(t, entryMap("findmy", body, len(body)))}))
	e.SHA256 = strings.Repeat("0", 64)
	st := &fakeStore{}
	fetch := mapFetcher{"https://skills.nousresearch.com/skills/apple/findmy/SKILL.md": body}
	h := newTestHub(t, fetch, st, &Config{})
	if _, err := h.Download(t.Context(), SourceOfficial, e); !errors.Is(err, ErrHash) {
		t.Fatalf("Download = %v, want ErrHash", err)
	}
	if len(st.quarantined) != 0 || len(st.indexed) != 0 {
		t.Errorf("hash mismatch wrote %d quarantined / %d indexed", len(st.quarantined), len(st.indexed))
	}
}

// TestF8SkillHubIndexValidation proves the mandatory ToS, digest, path and
// size fields, and the bounded body rules of the parser.
func TestF8SkillHubIndexValidation(t *testing.T) {
	t.Parallel()
	body := skillBody
	good := func() map[string]any { return entryMap("findmy", body, len(body)) }
	cases := []struct {
		name string
		doc  func() string
	}{
		{"missing tos", func() string {
			e := good()
			delete(e, "tos_note")
			return indexDoc(t, e)
		}},
		{"missing hash", func() string {
			e := good()
			delete(e, "sha256")
			return indexDoc(t, e)
		}},
		{"bad size", func() string {
			e := good()
			e["size"] = MaxSkillBytes + 1
			return indexDoc(t, e)
		}},
		{"traversal path", func() string {
			e := good()
			e["path"] = "../evil/SKILL.md"
			return indexDoc(t, e)
		}},
		{"absolute path", func() string {
			e := good()
			e["path"] = "/etc/SKILL.md"
			return indexDoc(t, e)
		}},
		{"unknown schema", func() string {
			return mustJSON(t, map[string]any{"schema": 2, "entries": []any{good()}})
		}},
		{"duplicate name", func() string {
			return indexDoc(t, good(), good())
		}},
	}
	for _, tc := range cases {
		if _, err := ParseIndex(SourceOfficial, []byte(tc.doc())); !errors.Is(err, ErrIndex) {
			t.Errorf("%s: ParseIndex = %v, want ErrIndex", tc.name, err)
		}
	}
	idx, err := ParseIndex(SourceOfficial, []byte(indexDoc(t, good())))
	if err != nil {
		t.Fatalf("valid index: %v", err)
	}
	if _, err := idx.Find("findmy"); err != nil {
		t.Errorf("Find: %v", err)
	}
	if _, err := idx.Find("missing"); !errors.Is(err, ErrNoEntry) {
		t.Errorf("Find(missing) = %v", err)
	}
}

// TestF8SkillHubSearch proves filtering, sorting and the limit of Search.
func TestF8SkillHubSearch(t *testing.T) {
	t.Parallel()
	a := entryMap("alpha", skillBody, len(skillBody))
	a["description"] = "Alpha helper."
	b := entryMap("findmy", skillBody, len(skillBody))
	b["description"] = "Find devices."
	fetch := mapFetcher{"https://skills.nousresearch.com/index.json": indexDoc(t, b, a)}
	h := newTestHub(t, fetch, &fakeStore{}, &Config{})
	got, err := h.Search(t.Context(), SourceOfficial, "", 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 2 || got[0].Name != "alpha" || got[1].Name != "findmy" {
		t.Fatalf("Search = %v", got)
	}
	got, err = h.Search(t.Context(), SourceOfficial, "device", 1)
	if err != nil || len(got) != 1 || got[0].Name != "findmy" {
		t.Fatalf("filtered Search = %v, %v", got, err)
	}
	if _, err := h.Search(t.Context(), unknownSource(t), "", 1); !errors.Is(err, ErrSource) {
		t.Errorf("unknown source = %v, want ErrSource", err)
	}
}

func unknownSource(t *testing.T) Source {
	t.Helper()
	src, err := ParseSource("nope")
	if err == nil {
		t.Fatalf("ParseSource(nope) succeeded")
	}
	return src
}

// TestF8SkillHubRetry proves the bounded retry and the rate-limit wait.
func TestF8SkillHubRetry(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	fail := fetcherFunc(func(context.Context, *url.URL, int64) ([]byte, error) {
		if calls.Add(1) < 3 {
			return nil, ErrStatus
		}
		return []byte("ok"), nil
	})
	bf, err := NewBoundedFetcher(fail, 0)
	if err != nil {
		t.Fatalf("NewBoundedFetcher: %v", err)
	}
	bf.Sleep = func(context.Context, time.Duration) error { return nil }
	u := mustURL(t, "https://skills.nousresearch.com/index.json")
	if body, err := bf.Get(t.Context(), u, 16); err != nil || string(body) != "ok" {
		t.Fatalf("retry = %q, %v", body, err)
	}
	if calls.Load() != 3 {
		t.Errorf("calls = %d, want 3", calls.Load())
	}
	if _, err := NewBoundedFetcher(nil, 0); !errors.Is(err, ErrURL) {
		t.Errorf("nil fetcher = %v", err)
	}
}

type fetcherFunc func(context.Context, *url.URL, int64) ([]byte, error)

func (f fetcherFunc) Get(ctx context.Context, u *url.URL, limit int64) ([]byte, error) {
	return f(ctx, u, limit)
}

// TestF8SKILL012Official proves the official source is registered, its
// default is https+allowlisted, a pinned sha256 is mandatory, and an invalid
// override fails instead of silently falling back to the default.
func TestF8SKILL012Official(t *testing.T) {
	t.Parallel()
	base, ok := DefaultBaseURL(SourceOfficial)
	if !ok || !strings.HasPrefix(base, "https://") {
		t.Fatalf("official base = %q, ok=%v", base, ok)
	}
	idx := entryMap("findmy", skillBody, len(skillBody))
	delete(idx, "sha256")
	if _, err := ParseIndex(SourceOfficial, []byte(indexDoc(t, idx))); !errors.Is(err, ErrIndex) {
		t.Errorf("entry without sha256 = %v, want ErrIndex", err)
	}
	if _, err := New(&Config{
		Fetcher: mapFetcher{}, Service: newTestService(t, &fakeStore{}),
		Sources: map[Source]string{SourceOfficial: "http://skills.nousresearch.com"},
	}); !errors.Is(err, ErrURL) {
		t.Errorf("official http downgrade = %v, want ErrURL (no fallback)", err)
	}
}

// TestF8SKILL013GitHub proves the GitHub source requires a commit SHA and
// verifies the pinned sha256 of the content.
func TestF8SKILL013GitHub(t *testing.T) {
	t.Parallel()
	body := skillBody
	base := entryMap("findmy", body, len(body))
	base["path"] = "repos/acme/skills/contents/oracle/SKILL.md"
	entryPath, ok := base["path"].(string)
	if !ok {
		t.Fatal("path is not a string")
	}
	if _, err := ParseIndex(SourceGitHub, []byte(indexDoc(t, base))); !errors.Is(err, ErrIndex) {
		t.Errorf("GitHub entry without commit = %v, want ErrIndex", err)
	}
	base["commit"] = strings.Repeat("a", 40)
	idx, err := ParseIndex(SourceGitHub, []byte(indexDoc(t, base)))
	if err != nil {
		t.Fatalf("pinned GitHub entry: %v", err)
	}
	fetch := mapFetcher{"https://api.github.com/" + entryPath: body}
	st := &fakeStore{}
	h := newTestHub(t, fetch, st, &Config{})
	inst, err := h.Download(t.Context(), SourceGitHub, &idx.Entries[0])
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if inst.Status != skills.StatusScanned {
		t.Errorf("status = %q, want scanned", inst.Status)
	}
	idx.Entries[0].SHA256 = strings.Repeat("0", 64)
	if _, err := h.Download(t.Context(), SourceGitHub, &idx.Entries[0]); !errors.Is(err, ErrHash) {
		t.Errorf("GitHub bad hash = %v, want ErrHash", err)
	}
}

// TestF8SKILL014ClawHub proves a ClawHub entry is quarantined (pending), not
// trusted, and cannot be retrieved before approval.
func TestF8SKILL014ClawHub(t *testing.T) {
	t.Parallel()
	body := skillBody
	e := entryMap("findmy", body, len(body))
	fetch := mapFetcher{
		"https://clawhub.ai/index.json":                   indexDoc(t, e),
		"https://clawhub.ai/skills/apple/findmy/SKILL.md": body,
	}
	st := &fakeStore{}
	h := newTestHub(t, fetch, st, &Config{})
	idx, err := h.Index(t.Context(), SourceClawHub)
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	inst, err := h.Download(t.Context(), SourceClawHub, &idx.Entries[0])
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if inst.Status != skills.StatusScanned || st.approved {
		t.Fatalf("ClawHub status=%q approved=%v", inst.Status, st.approved)
	}
	svc := newTestService(t, st)
	if got, rerr := svc.Retrieve(t.Context(), "findmy", 3); rerr != nil || len(got) != 0 {
		t.Fatalf("ClawHub pending retrievable: %v %v", got, rerr)
	}
}

// TestF8SKILL015SkillsSh proves the rate-limit wait and the bounded retry of
// the skills.sh source wrapper.
func TestF8SKILL015SkillsSh(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	var waits []time.Duration
	fail := fetcherFunc(func(context.Context, *url.URL, int64) ([]byte, error) {
		calls.Add(1)
		return nil, ErrStatus
	})
	bf, err := NewBoundedFetcher(fail, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("NewBoundedFetcher: %v", err)
	}
	clock := time.Unix(100, 0)
	bf.Now = func() time.Time { return clock }
	bf.last = clock
	bf.Sleep = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		clock = clock.Add(d)
		return nil
	}
	u := mustURL(t, "https://skills.sh/index.json")
	if _, err := bf.Get(t.Context(), u, 64); !errors.Is(err, ErrStatus) {
		t.Errorf("exhausted retry = %v, want ErrStatus", err)
	}
	if int(calls.Load()) != bf.Attempts {
		t.Errorf("calls = %d, want %d", calls.Load(), bf.Attempts)
	}
	if len(waits) == 0 || waits[0] != 50*time.Millisecond {
		t.Errorf("rate-limit waits = %v, want first 50ms", waits)
	}
}
