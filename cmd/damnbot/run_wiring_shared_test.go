package main

import (
	"context"
	"testing"

	"github.com/damnbot/damnbot/internal/codetools"
	"github.com/damnbot/damnbot/internal/ctxdb"
)

// TestRunWiringBorrowsSharedStore is the regression for the F9 soak RSS gate:
// a gateway turn must run on the serve store instead of opening a fresh
// ctxdb.Store per message (which retained one migration ladder per turn and
// grew RSS without bound), must not close the borrowed store, and must still
// release its session lease so `damnbot undo` can take it.
func TestRunWiringBorrowsSharedStore(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: dir, Kind: "cli"})
	if err != nil {
		t.Fatalf("open shared store: %v", err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close shared store: %v", cerr)
		}
	}()
	sid, _, err := newRunSession(ctx, db, "openai/gpt-fake", "cfg", "", "gateway")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	o := &runOptions{session: sid, sharedDB: db}
	got, owned, gotSID, _, model, err := openWiredSession(ctx, dir, "", "cfg", o, nil)
	if err != nil {
		t.Fatalf("openWiredSession: %v", err)
	}
	if owned {
		t.Fatal("owned = true for a borrowed store")
	}
	if got != db {
		t.Fatal("openWiredSession opened a second store instead of borrowing")
	}
	if gotSID != sid || model != "openai/gpt-fake" {
		t.Fatalf("session=%q model=%q, want %q/openai/gpt-fake", gotSID, model, sid)
	}
	// closeWiring is the fail/close path of a turn: it must release the lease
	// without closing the shared store.
	if cerr := closeWiring(ctx, nil, &runModules{}, &codetools.Engine{}, nil, nil, db, gotSID, owned); cerr != nil {
		t.Fatalf("closeWiring: %v", cerr)
	}
	if err := db.Write(ctx, func(*ctxdb.Tx) error { return nil }); err != nil {
		t.Fatalf("borrowed store closed by closeWiring: %v", err)
	}
	if _, err := db.AcquireLease(ctx, sid); err != nil {
		t.Fatalf("lease not released: %v", err)
	}
}
