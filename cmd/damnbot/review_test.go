package main

import (
	"bytes"
	"context"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/memory"
	"github.com/damnbot/damnbot/internal/redact"
)

// withMemory opens the production memory wiring over data and runs fn.
func withMemory(t *testing.T, data string, fn func(svc *memory.Service)) {
	t.Helper()
	db, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: data, Kind: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			t.Error(cerr)
		}
	}()
	svc, err := openMemory(db, redact.New(redact.Options{}))
	if err != nil {
		t.Fatal(err)
	}
	fn(svc)
}

// seedImports proposes import-origin entries (always quarantined) and
// returns their ids.
func seedImports(t *testing.T, data, scope string, contents ...string) []int64 {
	t.Helper()
	var ids []int64
	withMemory(t, data, func(svc *memory.Service) {
		for _, c := range contents {
			rec, err := svc.Propose(context.Background(), &memory.Proposal{
				Category: memory.CategoryFact, Scope: scope, Content: c, Origin: memory.OriginImport,
			})
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, rec.ID)
		}
	})
	return ids
}

func reviewCLI(t *testing.T, code int, args ...string) string {
	t.Helper()
	var out, errb bytes.Buffer
	if got := run(append([]string{"review"}, args...), &out, &errb); got != code {
		t.Fatalf("review %q: exit %d, want %d; stdout %q stderr %q", args, got, code, out.String(), errb.String())
	}
	return out.String()
}

func idStr(id int64) string { return strconv.FormatInt(id, 10) }

// TestReviewApproveReject: quarantined entries are listed, decided one by one
// or in bulk by the user, and only approved ones become retrievable (P10).
func TestReviewApproveReject(t *testing.T) {
	t.Parallel()
	data := t.TempDir()
	ids := seedImports(t, data, "user", "alpha fact", "beta fact", "gamma fact")
	out := reviewCLI(t, 0, "--scope", "user", "--data-dir", data)
	for _, id := range ids {
		if !strings.Contains(out, idStr(id)+"\tscanned\tuser\tfact\timport\t") {
			t.Fatalf("pending list lacks %d: %q", id, out)
		}
	}
	if got := reviewCLI(t, 0, "approve", idStr(ids[0]), "--scope", "user", "--data-dir", data); got != idStr(ids[0])+" approved\n" {
		t.Fatalf("approve output %q", got)
	}
	reviewCLI(t, 0, "reject", idStr(ids[1]), "--scope", "user", "--data-dir", data)
	if got := reviewCLI(t, 0, "--rejected", "--scope", "user", "--data-dir", data); !strings.Contains(got, idStr(ids[1])+"\trejected") || strings.Contains(got, idStr(ids[0])+"\t") {
		t.Fatalf("rejected list %q", got)
	}
	reviewCLI(t, 1, "approve", idStr(ids[1]), "--scope", "user", "--data-dir", data) // no longer pending
	if got := reviewCLI(t, 0, "approve", "--bulk", "--scope", "user", "--data-dir", data); got != idStr(ids[2])+" approved\n" {
		t.Fatalf("bulk output %q", got)
	}
	withMemory(t, data, func(svc *memory.Service) {
		hits, err := svc.Recall(context.Background(), &memory.Recall{Text: "fact", Scopes: []string{"user"}})
		if err != nil || len(hits) != 2 {
			t.Fatalf("recall after review = %d %v", len(hits), err)
		}
		for _, h := range hits {
			if h.Record.ID == ids[1] {
				t.Fatalf("rejected entry retrievable: %+v", h)
			}
		}
	})
}

// TestReviewScopes: the default scopes are user + project:<cwd>; an entry of
// another scope cannot be decided from this review (JN3-1).
func TestReviewScopes(t *testing.T) {
	t.Parallel()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	here := seedImports(t, data, "project:"+cwd, "local fact")
	other := seedImports(t, data, "project:/elsewhere", "foreign fact")
	if out := reviewCLI(t, 0, "--data-dir", data); !strings.Contains(out, idStr(here[0])+"\t") || strings.Contains(out, "foreign") {
		t.Fatalf("default scopes list %q", out)
	}
	reviewCLI(t, 1, "approve", idStr(other[0]), "--data-dir", data)
	reviewCLI(t, 0, "approve", idStr(other[0]), "--scope", "project:/elsewhere", "--data-dir", data)
}

// TestReviewSanitizesContent: rejected text cannot emit terminal escapes.
func TestReviewSanitizesContent(t *testing.T) {
	t.Parallel()
	data := t.TempDir()
	ids := seedImports(t, data, "user", "paint \x1b[31mred\x1b[0m\nnext line")
	reviewCLI(t, 0, "reject", idStr(ids[0]), "--scope", "user", "--data-dir", data)
	out := reviewCLI(t, 0, "--rejected", "--scope", "user", "--data-dir", data)
	if strings.ContainsRune(out, 0x1b) || strings.Count(out, "\n") != 1 || !strings.Contains(out, "paint [31mred [0m next line") {
		t.Fatalf("unsanitized review output %q", out)
	}
	// REVIEW-F2-SEC5-6: bidi/format runes and a scope carrying newline/tab
	// are sanitized in every field: one line, exactly the 6 separators.
	scope := "project:/x\u202e\nFAKE\tROW"
	seedImports(t, data, scope, "isolate \u2066hidden\u2069 \u200b text")
	out = reviewCLI(t, 0, "--rejected", "--scope", scope, "--data-dir", data) // invisible runes scan high: auto-rejected
	if strings.ContainsAny(out, "\u202e\u2066\u2069\u200b") || strings.Count(out, "\n") != 1 || strings.Count(out, "\t") != 6 {
		t.Fatalf("bidi/scope not sanitized: %q", out)
	}
}

// TestReviewBeyondOnePage (REVIEW-F2-SEC5-7): the listing notes a full
// page, an explicit id past the first page can be decided, and --bulk
// drains the whole queue.
func TestReviewBeyondOnePage(t *testing.T) {
	t.Parallel()
	data := t.TempDir()
	contents := make([]string, memory.MaxRecall+5)
	for i := range contents {
		contents[i] = "fact " + strconv.Itoa(i)
	}
	ids := seedImports(t, data, "user", contents...)
	if out := reviewCLI(t, 0, "--scope", "user", "--data-dir", data); strings.Count(out, "\tscanned\t") != memory.MaxRecall || !strings.Contains(out, "--bulk covers all") {
		t.Fatalf("first page = %q", out)
	}
	reviewCLI(t, 0, "approve", idStr(ids[0]), "--scope", "user", "--data-dir", data) // resolved by id, not from the listed page
	if out := reviewCLI(t, 0, "reject", "--bulk", "--scope", "user", "--data-dir", data); strings.Count(out, " rejected\n") != len(ids)-1 {
		t.Fatalf("bulk decided %d of %d: %q", strings.Count(out, " rejected\n"), len(ids)-1, out)
	}
	if out := reviewCLI(t, 0, "--scope", "user", "--data-dir", data); out != "" {
		t.Fatalf("queue not drained: %q", out)
	}
}

// TestReviewParseErrors: actions need ids xor --bulk; ids need an action.
func TestReviewParseErrors(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"approve"}, {"approve", "1", "--bulk"}, {"7"}, {"--bulk"}, {"approve", "x"}, {"merge", "1"}, {"reject", "1", "--rejected"},
	} {
		var out, errb bytes.Buffer
		if code := run(append([]string{"review"}, args...), &out, &errb); code != 2 || out.Len() != 0 {
			t.Errorf("review %q: exit %d stdout %q", args, code, out.String())
		}
	}
}
