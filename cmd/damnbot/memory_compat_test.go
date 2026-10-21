package main

import (
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/memory"
	"github.com/damnbot/damnbot/internal/redact"
)

func newMemService(t *testing.T) *memory.Service {
	t.Helper()
	store, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: t.TempDir(), Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := store.Close(); cerr != nil {
			t.Error(cerr)
		}
	})
	red := redact.New(redact.Options{})
	svc, err := memory.New(memAdapter{s: store, red: red}, memory.Config{Now: func() int64 { return 1_000 }, Redactor: red})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// TestMemoryServiceOverMemstore: the quarantine gate holds through real SQL:
// a written entry is invisible to Recall until approved, and approval needs a
// real decision row.
func TestMemoryServiceOverMemstore(t *testing.T) {
	ctx := t.Context()
	svc := newMemService(t)
	rec, err := svc.Propose(ctx, &memory.Proposal{
		Category: memory.CategoryFact, Scope: "user", Content: "decision log lives in ctxdb",
		Origin: memory.OriginImport,
	})
	if err != nil {
		t.Fatal(err)
	}
	hits, err := svc.Recall(ctx, &memory.Recall{Text: "ctxdb", Scopes: []string{"user"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("quarantined entry retrieved: %+v", hits)
	}
	if err = svc.Approve(ctx, rec.ID, "user"); err != nil {
		t.Fatal(err)
	}
	if hits, err = svc.Recall(ctx, &memory.Recall{Text: "ctxdb", Scopes: []string{"user"}}); err != nil || len(hits) != 1 {
		t.Fatalf("approved entry: %d %v", len(hits), err)
	}
}

// TestMemoryRedactionOverMemstore: redaction happens before the FTS index and
// the stored row.
func TestMemoryRedactionOverMemstore(t *testing.T) {
	ctx := t.Context()
	svc := newMemService(t)
	secret := "ghp_" + strings.Repeat("C", 36)
	user, err := svc.Propose(ctx, &memory.Proposal{
		Category: memory.CategoryPreference, Scope: "user", Content: "token " + secret,
		Origin: memory.OriginUser,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(user.Content, secret) || !redact.ContainsSentinel(user.Content) {
		t.Fatalf("proposal leaked the secret: %q", user.Content)
	}
	got, err := svc.Get(ctx, []int64{user.ID})
	if err != nil || len(got) != 1 || strings.Contains(got[0].Content, secret) {
		t.Fatalf("stored secret: %+v %v", got, err)
	}
}
