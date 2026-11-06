package kanban

import (
	"errors"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// TestStatusClosedSet proves the schema's closed status set and the live
// predicate the model promises to later slices.
func TestStatusClosedSet(t *testing.T) {
	t.Parallel()
	for _, s := range []Status{StatusBacklog, StatusReady, StatusInProgress, StatusReview, StatusDone, StatusBlocked} {
		if !s.valid() {
			t.Errorf("%q not valid", s)
		}
		if want := s != StatusDone; s.live() != want {
			t.Errorf("%q live = %t, want %t", s, s.live(), want)
		}
	}
	if Status("archived").valid() {
		t.Error("archived is not a seventh status")
	}
}

// TestBoardLookup proves boardID resolves both the id and the name inside the
// write transaction and reports a missing board as ErrBoardNotFound.
func TestBoardLookup(t *testing.T) {
	t.Parallel()
	db, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: t.TempDir(), Kind: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	})
	store := New(db, t.TempDir())
	board, err := store.InitBoard(t.Context(), "default", "u")
	if err != nil {
		t.Fatal(err)
	}
	var byID, byName string
	if werr := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		var xerr error
		if byID, xerr = boardID(tx, board.ID); xerr != nil {
			return xerr
		}
		byName, xerr = boardID(tx, "default")
		return xerr
	}); werr != nil {
		t.Fatal(werr)
	}
	if byID != board.ID || byName != board.ID {
		t.Fatalf("boardID = %q/%q, want %q", byID, byName, board.ID)
	}
	boards, err := store.Boards(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(boards) != 1 || boards[0].ID != board.ID || boards[0].Name != "default" {
		t.Fatalf("Boards() = %+v", boards)
	}
	err = db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		_, xerr := boardID(tx, "missing")
		return xerr
	})
	if !errors.Is(err, ErrBoardNotFound) {
		t.Fatalf("missing board err = %v", err)
	}
}
