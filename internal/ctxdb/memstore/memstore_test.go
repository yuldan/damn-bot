package memstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"go.uber.org/goleak"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/redact"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func open(t *testing.T, dir string) *ctxdb.Store {
	t.Helper()
	s, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: dir, Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := s.Close(); cerr != nil {
			t.Error(cerr)
		}
	})
	return s
}

func redactor() *redact.Redactor { return redact.New(redact.Options{}) }

func projectScope(t *testing.T) string { return "project:" + t.TempDir() }

func mkrow(scope, content, origin string) *Row {
	return &Row{Category: "fact", Scope: scope, Content: content, Origin: origin, ScanResult: `{"severity":"low"}`, CreatedAt: 100}
}

func approved() *Decision {
	return &Decision{Status: "approved", Actor: "user", Reason: "user", CreatedAt: 100}
}

func put(t *testing.T, s *ctxdb.Store, r *Row, d *Decision) int64 {
	t.Helper()
	id, err := Put(t.Context(), s, r, d, redactor())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func search(t *testing.T, s *ctxdb.Store, q *Query) []Hit {
	t.Helper()
	hits, err := Search(t.Context(), s, q)
	if err != nil {
		t.Fatal(err)
	}
	return hits
}

// TestQuarantineInvisible: scanned content is stored but never returned
// until approved.
func TestQuarantineInvisible(t *testing.T) {
	s := open(t, t.TempDir())
	id := put(t, s, mkrow("user", "deploy checklist", "import"),
		&Decision{Status: "scanned", Actor: "system", Reason: "scan:low", CreatedAt: 100})
	if hits := search(t, s, &Query{Text: "deploy", Scopes: []string{"user"}, Now: 200}); len(hits) != 0 {
		t.Fatalf("quarantined row retrieved: %+v", hits)
	}
	if err := Decide(t.Context(), s, id, "user", approved()); err != nil {
		t.Fatal(err)
	}
	hits := search(t, s, &Query{Text: "deploy", Scopes: []string{"user"}, Now: 200})
	if len(hits) != 1 || hits[0].Row.ID != id || hits[0].Row.Status != "approved" {
		t.Fatalf("approved row: %+v", hits)
	}
}

// TestRedactionAtStorage: a secret passed straight to the storage boundary is
// redacted before the INSERT and therefore absent from the FTS index too.
func TestRedactionAtStorage(t *testing.T) {
	s := open(t, t.TempDir())
	secret := "ghp_" + strings.Repeat("B", 36)
	id := put(t, s, mkrow("user", "token "+secret, "user"), approved())
	rows, err := Get(t.Context(), s, []int64{id})
	if err != nil || len(rows) != 1 {
		t.Fatalf("get: %v %v", rows, err)
	}
	if strings.Contains(rows[0].Content, secret) || !redact.ContainsSentinel(rows[0].Content) {
		t.Fatalf("content not redacted: %q", rows[0].Content)
	}
	if hits := search(t, s, &Query{Text: secret, Scopes: []string{"user"}, Now: 200}); len(hits) != 0 {
		t.Fatalf("secret indexed: %+v", hits)
	}
}

// TestScopeCap: 8 × 500 characters fill one scope; the ninth is TooLarge and
// another scope is unaffected.
func TestScopeCap(t *testing.T) {
	s := open(t, t.TempDir())
	full := strings.Repeat("x", MaxContentChars)
	for range 8 {
		put(t, s, mkrow("user", full, "user"), approved())
	}
	_, err := Put(t.Context(), s, mkrow("user", "overflow", "user"), approved(), redactor())
	if _, ok := errors.AsType[TooLarge](err); !ok {
		t.Fatalf("err = %v, want TooLarge", err)
	}
	put(t, s, mkrow(projectScope(t), "fits elsewhere", "user"), approved())
}

// TestTransitions: closed state machine and supersede retirement.
func TestTransitions(t *testing.T) {
	ctx := t.Context()
	s := open(t, t.TempDir())
	id := put(t, s, mkrow("user", "first", "user"), approved())
	if derr := Decide(ctx, s, id, "user", &Decision{Status: "rejected", Actor: "user", CreatedAt: 100}); !errors.Is(derr, ErrTransition) {
		t.Fatalf("approved -> rejected = %v", derr)
	}
	if derr := Decide(ctx, s, 999, "user", approved()); !errors.Is(derr, ErrNotFound) {
		t.Fatalf("missing row = %v", derr)
	}
	next := put(t, s, &Row{
		Category: "fact", Scope: "user", Content: "second", Origin: "user",
		SupersedesID: &id, ScanResult: "{}", CreatedAt: 100,
	}, approved())
	rows, err := Get(ctx, s, []int64{id})
	if err != nil || len(rows) != 1 || rows[0].Status != "retired" {
		t.Fatalf("superseded row: %+v %v", rows, err)
	}
	if derr := Decide(ctx, s, id, "user", approved()); !errors.Is(derr, ErrTransition) {
		t.Fatalf("retired -> approved = %v", derr)
	}
	if rows, err = Get(ctx, s, []int64{next}); err != nil || len(rows) != 1 || rows[0].Status != "approved" {
		t.Fatalf("new row: %+v %v", rows, err)
	}
}

// TestSearchFilters: scope, expiry and pin ordering.
func TestSearchFilters(t *testing.T) {
	s := open(t, t.TempDir())
	project := projectScope(t)
	a := put(t, s, mkrow("user", "alpha one", "user"), approved())
	b := put(t, s, mkrow(project, "alpha two", "user"), approved())
	all := []string{"user", project}
	old := int64(50)
	put(t, s, &Row{
		Category: "fact", Scope: "user", Content: "alpha old", Origin: "user",
		ExpiresAt: &old, ScanResult: "{}", CreatedAt: 100,
	}, approved())
	if err := Pin(t.Context(), s, b, project, true, "user", 100); err != nil {
		t.Fatal(err)
	}
	hits := search(t, s, &Query{Text: "alpha", Scopes: all, Now: 200})
	if len(hits) != 2 {
		t.Fatalf("text hits = %d, want 2", len(hits))
	}
	if hits[0].Row.ID != b {
		t.Fatalf("pinned row not first: %+v", hits)
	}
	if got := search(t, s, &Query{Text: "alpha", Scopes: []string{"user"}, Now: 200}); len(got) != 1 || got[0].Row.ID != a {
		t.Fatalf("scope filter: %+v", got)
	}
	if got := search(t, s, &Query{Scopes: all, Now: 200}); len(got) != 2 || got[0].Row.ID != b {
		t.Fatalf("empty query order: %+v", got)
	}
	// FTS operators in user text are separators, not a MATCH syntax error;
	// candidates are OR-ed, so both rows share the "alpha" term.
	if got := search(t, s, &Query{Text: `alpha - ("one`, Scopes: all, Now: 200}); len(got) != 2 {
		t.Fatalf("punctuation query: %+v", got)
	}
}

// TestFTSQueryDedupesAndCaps: duplicate terms are scored once and the
// expression never exceeds maxFTSTerms (a long message cannot blow up MATCH).
func TestFTSQueryDedupesAndCaps(t *testing.T) {
	q := ftsQuery(strings.Repeat("alpha ", 10) + strings.Repeat("beta ", 10))
	if strings.Count(q, `"alpha"`) != 1 || strings.Count(q, `"beta"`) != 1 {
		t.Fatalf("duplicate terms not deduped: %q", q)
	}
	var b strings.Builder
	for i := range 100 {
		fmt.Fprintf(&b, "t%d ", i)
	}
	q = ftsQuery(b.String())
	if n := strings.Count(q, `"`) / 2; n != 64 {
		t.Fatalf("term count = %d, want 64", n)
	}
	if ftsQuery("   ") != "" {
		t.Fatal("blank query is not empty")
	}
}

// TestSearchQuestionRecallsFact: a natural-language question must reach the
// stored fact even though it shares only some words with it (BM25 candidate
// selection is OR, SPEC §4.8/B-03); an absent term recalls nothing.
func TestSearchQuestionRecallsFact(t *testing.T) {
	s := open(t, t.TempDir())
	id := put(t, s, mkrow("user", "my locker number is 4417", "user"), approved())
	for _, q := range []string{"What is my locker number?", "locker number", "4417"} {
		hits := search(t, s, &Query{Text: q, Scopes: []string{"user"}, Now: 200})
		if len(hits) != 1 || hits[0].Row.ID != id {
			t.Fatalf("query %q hits = %+v, want row %d", q, hits, id)
		}
	}
	if hits := search(t, s, &Query{Text: "passport", Scopes: []string{"user"}, Now: 200}); len(hits) != 0 {
		t.Fatalf("absent term recalled: %+v", hits)
	}
}

// TestSearchRanksByRareTerm: terms shared by every row ("my", "is") must not
// bury the row that matches the question's distinctive words.
func TestSearchRanksByRareTerm(t *testing.T) {
	s := open(t, t.TempDir())
	put(t, s, mkrow("user", "my locker number is 4417", "user"), approved())
	plate := put(t, s, mkrow("user", "my car plate is B 7731 KQ", "user"), approved())
	hits := search(t, s, &Query{Text: "What is my car plate?", Scopes: []string{"user"}, Now: 200})
	if len(hits) == 0 || hits[0].Row.ID != plate {
		t.Fatalf("question ranking = %+v, want row %d first", hits, plate)
	}
}

// TestSearchOperatorWordsAreTerms: a user word that spells an FTS operator is
// still a term; the expression is built from quoted, normalized tokens.
func TestSearchOperatorWordsAreTerms(t *testing.T) {
	s := open(t, t.TempDir())
	id := put(t, s, mkrow("user", "orange or lemon", "user"), approved())
	if hits := search(t, s, &Query{Text: "orange or lemon", Scopes: []string{"user"}, Now: 200}); len(hits) != 1 || hits[0].Row.ID != id {
		t.Fatalf("operator-word query = %+v, want row %d", hits, id)
	}
	if hits := search(t, s, &Query{Text: `" (NEAR)`, Scopes: []string{"user"}, Now: 200}); len(hits) != 0 {
		t.Fatalf("operator-only query = %+v, want no syntax error and no hits", hits)
	}
}

// TestExpire: expired rows are hidden and can be reconfirmed.
func TestExpire(t *testing.T) {
	ctx := t.Context()
	s := open(t, t.TempDir())
	past := int64(150)
	id := put(t, s, &Row{
		Category: "fact", Scope: "user", Content: "short lived", Origin: "user",
		ExpiresAt: &past, ScanResult: "{}", CreatedAt: 100,
	}, approved())
	n, err := Expire(ctx, s, 200)
	if err != nil || n != 1 {
		t.Fatalf("expire = %d %v", n, err)
	}
	rows, err := Get(ctx, s, []int64{id})
	if err != nil || len(rows) != 1 || rows[0].Status != "expired" {
		t.Fatalf("expired row: %+v %v", rows, err)
	}
	if err := Decide(ctx, s, id, "user", approved()); err != nil {
		t.Fatalf("reconfirm: %v", err)
	}
}

// TestCrossProcess: two OS processes append to one data dir; the writer lock
// serialises them and every row is accounted for (memstore on ctxdb).
func TestCrossProcess(t *testing.T) {
	dir := t.TempDir()
	init, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: dir, Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	if err = init.Close(); err != nil {
		t.Fatal(err)
	}
	var cmds []*exec.Cmd
	for range 2 {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestChildMemory$", "-test.count=1") //nolint:gosec // G204: re-exec of this test binary
		cmd.Env = append(os.Environ(), childDirEnv+"="+dir)
		cmd.Stderr = os.Stderr
		if serr := cmd.Start(); serr != nil {
			t.Fatal(serr)
		}
		cmds = append(cmds, cmd)
	}
	for _, cmd := range cmds {
		if werr := cmd.Wait(); werr != nil {
			t.Fatalf("child: %v", werr)
		}
	}
	s := open(t, dir)
	n, err := s.Count(t.Context(), "SELECT count(*) FROM memory WHERE kind = 'memory'")
	if err != nil {
		t.Fatal(err)
	}
	if n != 40 {
		t.Fatalf("rows = %d, want 40", n)
	}
	if m, cerr := s.Count(t.Context(), `SELECT count(*) FROM (SELECT content FROM memory GROUP BY content HAVING count(*) > 1)`); cerr != nil || m != 0 {
		t.Fatalf("duplicate content = %d %v", m, cerr)
	}
}

const childDirEnv = "MEMSTORE_CHILD_DIR"

// TestChildMemory is the helper process of TestCrossProcess (same pattern as
// ctxdb's TestChildProcess; a plain return, not a skip, outside it).
func TestChildMemory(t *testing.T) {
	dir := os.Getenv(childDirEnv)
	if dir == "" {
		return
	}
	s, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: dir, Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := s.Close(); cerr != nil {
			t.Error(cerr)
		}
	}()
	pid := os.Getpid()
	for i := range 20 {
		r := mkrow("user", fmt.Sprintf("child entry %d %d", pid, i), "user")
		if _, err := Put(t.Context(), s, r, approved(), redactor()); err != nil {
			t.Fatal(err)
		}
	}
}

// TestScopeBoundaries: empty scopes deny and cross-scope ids fail closed
// (JN3-1/JN3-2).
func TestScopeBoundaries(t *testing.T) {
	ctx := t.Context()
	s := open(t, t.TempDir())
	project := projectScope(t)
	put(t, s, mkrow("user", "alpha alice", "user"), approved())
	b := put(t, s, mkrow(project, "alpha bob", "user"), approved())
	if err := Decide(ctx, s, b, "user", approved()); !errors.Is(err, ErrScope) {
		t.Fatalf("cross-scope decide = %v", err)
	}
	if err := Pin(ctx, s, b, "user", true, "user", 100); !errors.Is(err, ErrScope) {
		t.Fatalf("cross-scope pin = %v", err)
	}
	_, err := Put(ctx, s, &Row{
		Category: "fact", Scope: "user", Content: "steal", Origin: "user",
		SupersedesID: &b, ScanResult: "{}", CreatedAt: 100,
	}, approved(), redactor())
	if !errors.Is(err, ErrScope) {
		t.Fatalf("cross-scope supersede = %v", err)
	}
	if hits := search(t, s, &Query{Text: "alpha", Now: 200}); len(hits) != 0 {
		t.Fatalf("empty scopes must deny: %+v", hits)
	}
}

// TestSkillRowsExcluded: approved memory(kind='skill') rows never enter the
// memory recall path (JN3-3).
func TestSkillRowsExcluded(t *testing.T) {
	ctx := t.Context()
	s := open(t, t.TempDir())
	var id int64
	err := s.Write(ctx, func(tx *ctxdb.Tx) error {
		if xerr := tx.Get(`INSERT INTO memory(kind, category, scope, name, content, origin, scan_result, created_at)
			VALUES ('skill', 'fact', 'user', 's', 'skill body text', 'user', '{}', 1) RETURNING id`, []any{}, &id); xerr != nil {
			return fmt.Errorf("insert skill row: %w", xerr)
		}
		if _, xerr := tx.Exec(`INSERT INTO memory_decisions(memory_id, status, actor, reason, created_at)
			VALUES (?1, 'approved', 'user', 'confirmed', 1)`, id); xerr != nil {
			return fmt.Errorf("insert decision: %w", xerr)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if hits := search(t, s, &Query{Text: "skill", Scopes: []string{"user"}, Now: 200}); len(hits) != 0 {
		t.Fatalf("skill row returned: %+v", hits)
	}
}

// TestPolicyRejectsApprovedUntrusted: the store itself refuses an initial
// approved decision for agent/import content (JN3-8).
func TestPolicyRejectsApprovedUntrusted(t *testing.T) {
	s := open(t, t.TempDir())
	_, err := Put(t.Context(), s, mkrow("user", "x", "import"), approved(), redactor())
	if !errors.Is(err, ErrPolicy) {
		t.Fatalf("err = %v, want ErrPolicy", err)
	}
}
