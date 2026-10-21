package skills

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const md = `---
name: sdlc-review
description: Review a change against the SDLC checklist
---
# Review

Read the diff and check the checklist.
`

func parse(t *testing.T, data string, src Source) Skill {
	t.Helper()
	sk, err := Parse([]byte(data), "/skills/sdlc-review/SKILL.md", src)
	if err != nil {
		t.Fatal(err)
	}
	return sk
}

// TestParse: front matter variants and the closed shape.
func TestParse(t *testing.T) {
	sk := parse(t, md, SourceUser)
	if sk.Name != "sdlc-review" || !strings.Contains(sk.Body, "checklist") || len(sk.SHA256) != 64 {
		t.Fatalf("parse: %+v", sk)
	}
	crlf := strings.ReplaceAll(strings.ReplaceAll(md, "\n", "\r\n"), `"`, `'`)
	sk = parse(t, crlf, SourceBuiltin)
	if sk.Name != "sdlc-review" || sk.Description != "Review a change against the SDLC checklist" {
		t.Fatalf("crlf parse: %+v", sk)
	}
	quoted := `---
name: "quoted-name"
description: 'quoted description'
---
body`
	if sk = parse(t, quoted, SourceHub); sk.Name != "quoted-name" || sk.Description != "quoted description" {
		t.Fatalf("quoted parse: %+v", sk)
	}
	for _, name := range []string{"a", "sdlc-review", "google_workspace", strings.Repeat("a", MaxName)} {
		if !validName(name) {
			t.Errorf("name %q rejected", name)
		}
	}
	for _, name := range []string{"", "Upper", "-lead", "has space", strings.Repeat("a", MaxName+1)} {
		if validName(name) {
			t.Errorf("name %q accepted", name)
		}
	}
}

// TestParseErrors: every malformed input is rejected with a closed error.
func TestParseErrors(t *testing.T) {
	big := md + strings.Repeat("x", MaxBytes)
	cases := []struct {
		name string
		data string
		want error
	}{
		{"too large", big, ErrTooLarge},
		{"no opening", "name: x\n---\nbody", ErrFrontMatter},
		{"no closing", "---\nname: x\nbody", ErrFrontMatter},
		{"no name", "---\ndescription: d\n---\nbody", ErrInvalid},
		{"empty body", "---\nname: ok\ndescription: d\n---\n", ErrInvalid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Parse([]byte(c.data), "/p/SKILL.md", SourceUser); !errors.Is(err, c.want) {
				t.Errorf("err = %v, want %v", err, c.want)
			}
		})
	}
}

type fakeStore struct {
	nextID      int64
	indexed     []Skill
	quarantined *Skill
	scanResult  string
	rejected    bool
	approved    bool
	status      Status
	searchLimit int
}

func (f *fakeStore) Index(_ context.Context, s *Skill) (int64, error) {
	f.nextID++
	s.ID = f.nextID
	f.indexed = append(f.indexed, *s)
	return s.ID, nil
}

func (f *fakeStore) Quarantine(_ context.Context, s *Skill, scan string, rejected bool) (int64, int64, error) {
	f.quarantined, f.scanResult, f.rejected = s, scan, rejected
	f.nextID++
	s.ID = f.nextID
	f.indexed = append(f.indexed, *s)
	if rejected {
		f.status = StatusRejected
	} else {
		f.status = StatusScanned
	}
	return 7, s.ID, nil
}

func (f *fakeStore) Approve(_ context.Context, memoryID int64) error {
	if memoryID != 7 {
		return errors.New("wrong memory id")
	}
	f.approved, f.status = true, StatusApproved
	return nil
}

func (f *fakeStore) Search(_ context.Context, _ string, k int) ([]Skill, error) {
	f.searchLimit = k
	if !f.approved {
		return nil, nil
	}
	limit := min(k, len(f.indexed))
	return f.indexed[:limit], nil
}

func (f *fakeStore) Status(_ context.Context, _ int64) (Status, error) { return f.status, nil }

func newService(t *testing.T, st Store, scan Scanner) *Service {
	t.Helper()
	s, err := New(st, Config{Scan: scan, Redact: func(v string) string { return v }})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestInstallTrusted: user/builtin index directly with no review row.
func TestInstallTrusted(t *testing.T) {
	ctx := t.Context()
	st := &fakeStore{}
	svc := newService(t, st, nil)
	sk := parse(t, md, SourceUser)
	res, err := svc.Install(ctx, &sk)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusApproved || res.MemoryID != 0 || len(st.indexed) != 1 {
		t.Fatalf("trusted install: %+v %+v", res, st.indexed)
	}
}

func mediumScan(*Skill) (string, string, error) {
	return "medium", `{"severity":"medium"}`, nil
}

func highScan(*Skill) (string, string, error) { return "high", `{"severity":"high"}`, nil }

func install(t *testing.T, svc *Service, sk *Skill) Install {
	t.Helper()
	res, err := svc.Install(t.Context(), sk)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func statusOf(t *testing.T, svc *Service, memoryID int64) Status {
	t.Helper()
	st, err := svc.Status(t.Context(), memoryID)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// TestInstallQuarantine: a medium-severity proposal waits for approval and is
// absent from the prompt until then.
func TestInstallQuarantine(t *testing.T) {
	ctx := t.Context()
	st := &fakeStore{}
	svc := newService(t, st, mediumScan)
	sk := parse(t, md, SourceHub)
	res := install(t, svc, &sk)
	if res.Status != StatusScanned || res.MemoryID != 7 || st.rejected || st.scanResult == "" {
		t.Fatalf("medium install: %+v scan=%q rejected=%v", res, st.scanResult, st.rejected)
	}
	if got := statusOf(t, svc, res.MemoryID); got != StatusScanned {
		t.Fatalf("status after install: %q", got)
	}
	lines, err := svc.Offer(ctx, "review")
	if err != nil || len(lines) != 0 {
		t.Fatalf("quarantined offer: %v %v", lines, err)
	}
	if err = svc.Approve(ctx, 7); err != nil {
		t.Fatal(err)
	}
	if got := statusOf(t, svc, 7); got != StatusApproved {
		t.Fatalf("status after approve: %q", got)
	}
	lines, err = svc.Offer(ctx, "review")
	if err != nil || len(lines) != 1 || !strings.HasPrefix(lines[0], "- sdlc-review: ") {
		t.Fatalf("approved offer: %v %v", lines, err)
	}
}

// TestInstallRejected: a high-severity proposal is rejected and never
// offered.
func TestInstallRejected(t *testing.T) {
	st := &fakeStore{}
	svc := newService(t, st, highScan)
	sk := parse(t, md, SourceAgent)
	res := install(t, svc, &sk)
	if res.Status != StatusRejected || !st.rejected {
		t.Fatalf("high install: %+v rejected=%v", res, st.rejected)
	}
	lines, err := svc.Offer(t.Context(), "review")
	if err != nil || len(lines) != 0 {
		t.Fatalf("rejected offer: %v %v", lines, err)
	}
}

// TestInstallNeedsScanner: an untrusted source without a scanner fails
// closed.
func TestInstallNeedsScanner(t *testing.T) {
	svc := newService(t, &fakeStore{}, nil)
	sk := parse(t, md, SourceImport)
	if _, err := svc.Install(t.Context(), &sk); !errors.Is(err, ErrScan) {
		t.Fatalf("err = %v, want ErrScan", err)
	}
}

// TestOfferShapes: ≤120 description characters, one line, trailing counter.
func TestOfferShapes(t *testing.T) {
	long := strings.Repeat("word ", 100) + "\nsecond line"
	sk := Skill{
		ID: 1, Name: "sdlc-review", Description: long, Body: "x", Path: "/skills/x/SKILL.md",
		SHA256: strings.Repeat("a", 64), Source: SourceUser,
	}
	line := PromptLine(&sk)
	if strings.Contains(line, "\n") {
		t.Fatalf("newline in prompt line: %q", line)
	}
	desc := strings.TrimPrefix(strings.SplitN(line, " (", 2)[0], "- sdlc-review: ")
	if n := len([]rune(desc)); n > MaxPromptDesc {
		t.Fatalf("description %d runes > %d", n, MaxPromptDesc)
	}
	if !strings.Contains(line, "…") {
		t.Fatalf("long description not truncated: %q", line)
	}
}

// TestRetrieveLimit: k is clamped to the SPEC maximum of three.
func TestRetrieveLimit(t *testing.T) {
	ctx := t.Context()
	st := &fakeStore{}
	svc := newService(t, st, nil)
	if _, err := svc.Retrieve(ctx, "q", 100); err != nil {
		t.Fatal(err)
	}
	if st.searchLimit != MaxOffer {
		t.Fatalf("limit = %d, want %d", st.searchLimit, MaxOffer)
	}
	if err := svc.Approve(ctx, 0); !errors.Is(err, ErrInvalid) {
		t.Errorf("approve 0: %v", err)
	}
}

// TestRedactionAndRawScan: the scanner sees raw content, storage sees the
// redacted body (JN3-4/JN3-7).
func TestRedactionAndRawScan(t *testing.T) {
	st := &fakeStore{}
	scanned := ""
	scanner := func(s *Skill) (string, string, error) { scanned = s.Body; return "medium", "{}", nil }
	svc, err := New(st, Config{Scan: scanner, Redact: func(v string) string {
		return strings.ReplaceAll(v, "ghp_SECRET", "[REDACTED:secret]")
	}})
	if err != nil {
		t.Fatal(err)
	}
	sk := Skill{
		Name: "secret-skill", Description: "d", Body: "token ghp_SECRET", Path: "/p",
		SHA256: strings.Repeat("a", 64), Source: SourceHub,
	}
	if _, err = svc.Install(t.Context(), &sk); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(scanned, "ghp_SECRET") {
		t.Fatal("scanner did not see the raw secret")
	}
	if st.quarantined == nil || strings.Contains(st.quarantined.Body, "ghp_SECRET") ||
		!strings.Contains(st.quarantined.Body, "[REDACTED:") {
		t.Fatalf("stored body not redacted: %+v", st.quarantined)
	}
}

// TestBOMAndUnsafe: a BOM is accepted; control/bidi in the description or
// path is rejected and PromptLine neutralises parentheses (JN3-10/JN3-11).
func TestBOMAndUnsafe(t *testing.T) {
	if _, err := Parse([]byte("\xef\xbb\xbf"+md), "/p/SKILL.md", SourceUser); err != nil {
		t.Fatalf("BOM rejected: %v", err)
	}
	bidi := strings.Replace(md, "Review a change", "Rev\u202Eiew a change", 1)
	if _, err := Parse([]byte(bidi), "/p/SKILL.md", SourceUser); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bidi description = %v", err)
	}
	line := PromptLine(&Skill{
		Name: "x", Description: "d", Path: "/p) (evil",
		SHA256: strings.Repeat("a", 64), Source: SourceUser,
	})
	if strings.Contains(line, ") (evil") || !strings.Contains(line, "%29") {
		t.Fatalf("path not sanitized: %q", line)
	}
	if _, err := New(nil, Config{Redact: func(v string) string { return v }}); !errors.Is(err, ErrInvalid) {
		t.Errorf("nil store: %v", err)
	}
	if _, err := New(&fakeStore{}, Config{}); !errors.Is(err, ErrInvalid) {
		t.Errorf("nil redactor: %v", err)
	}
}
