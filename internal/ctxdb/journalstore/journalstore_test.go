package journalstore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"

	"go.uber.org/goleak"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/journal"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func sha(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func open(t *testing.T) (*ctxdb.Store, *Store) {
	t.Helper()
	ctx := t.Context()
	db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: t.TempDir(), Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Error(cerr)
		}
	})
	if _, serr := db.CreateSession(ctx, ctxdb.Session{ID: "s1", Origin: "headless", Cwd: "/"}); serr != nil {
		t.Fatal(serr)
	}
	js, err := New(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	return db, js
}

func writeRecord() *journal.Record {
	return &journal.Record{
		SessionID: "s1", TurnID: "t1", CallID: "c1", ToolName: "write_file",
		Kind: journal.FileWrite, Path: "/w/a.txt",
		Before: &journal.Snapshot{Data: []byte("old\n"), Mode: 0o644},
		After:  &journal.Snapshot{Data: []byte("new\n"), Mode: 0o644},
	}
}

func record(t *testing.T, js *Store, r *journal.Record) int64 {
	t.Helper()
	id, err := js.Record(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func checkRecordIdentity(t *testing.T, a *journal.Action) {
	t.Helper()
	if a.SessionID != "s1" || a.TurnID != "t1" || a.Kind != journal.FileWrite || a.Reversibility != journal.Reversible {
		t.Fatalf("action = %+v", a)
	}
	if a.Path != "/w/a.txt" || a.MoveTo != "" {
		t.Fatalf("paths = %q %q", a.Path, a.MoveTo)
	}
}

func checkRecordImages(t *testing.T, a *journal.Action) {
	t.Helper()
	if a.Before == nil || a.Before.BlobSHA != sha([]byte("old\n")) || a.Before.Mode != 0o644 || a.Before.Size != 4 {
		t.Fatalf("before = %+v", a.Before)
	}
	if a.After == nil || a.After.BlobSHA != sha([]byte("new\n")) {
		t.Fatalf("after = %+v", a.After)
	}
	if a.BytesHeld != 8 || a.Expired || a.Open {
		t.Fatalf("held/expired/open = %d/%v/%v", a.BytesHeld, a.Expired, a.Open)
	}
}

// TestRecordGetRoundTrip: the row and its inverse survive a round trip
// through the real database.
func TestRecordGetRoundTrip(t *testing.T) {
	t.Parallel()
	_, js := open(t)
	id := record(t, js, writeRecord())
	a, err := js.Get(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	checkRecordIdentity(t, &a)
	checkRecordImages(t, &a)
}

// TestRecordBlobsAndList: images are readable, List returns them newest
// first and a missing id is not_found.
func TestRecordBlobsAndList(t *testing.T) {
	t.Parallel()
	_, js := open(t)
	ctx := t.Context()
	id := record(t, js, writeRecord())
	a, err := js.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	for want, img := range map[string]*journal.Image{"old\n": a.Before, "new\n": a.After} {
		got, berr := js.Blob(ctx, img.BlobSHA)
		if berr != nil || string(got) != want {
			t.Fatalf("blob %s = %q, %v", img.BlobSHA, got, berr)
		}
	}
	list, err := js.List(ctx, "s1", 10)
	if err != nil || len(list) != 1 || list[0].ID != id {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if _, err := js.Get(ctx, id+1); !errors.Is(err, ctxdb.ErrNotFound) {
		t.Fatalf("missing action: %v", err)
	}
}

// TestRecordBlobRefs: both images are referenced as action blobs, so gc
// keeps them for the retention window (§11.6).
func TestRecordBlobRefs(t *testing.T) {
	t.Parallel()
	db, js := open(t)
	id := record(t, js, writeRecord())
	n, err := db.Count(t.Context(), "SELECT count(*) FROM blob_refs WHERE owner_kind = 'action' AND owner_id = ?", int64(id))
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("action blob refs = %d, want 2", n)
	}
	keys, err := db.Count(t.Context(), `SELECT count(DISTINCT ref_key) FROM blob_refs WHERE owner_kind = 'action' AND owner_id = ?`, int64(id))
	if err != nil || keys != 2 {
		t.Fatalf("distinct ref keys = %d, %v", keys, err)
	}
}

// TestFinishRecordsUndo: the undo event and action_undos row commit in one
// transaction.
func TestFinishRecordsUndo(t *testing.T) {
	t.Parallel()
	db, js := open(t)
	ctx := t.Context()
	id := record(t, js, writeRecord())
	if err := js.Finish(ctx, journal.Undo{ActionID: id, SessionID: "s1", Status: journal.Undone, Detail: "ok"}); err != nil {
		t.Fatal(err)
	}
	if n, err := db.Count(ctx, "SELECT count(*) FROM action_undos WHERE action_id = ?", id); err != nil || n != 1 {
		t.Fatalf("action_undos = %d, %v", n, err)
	}
	if n, err := db.Count(ctx, "SELECT count(*) FROM events WHERE kind = 'undo' AND session_id = 's1'"); err != nil || n != 1 {
		t.Fatalf("undo events = %d, %v", n, err)
	}
}

// TestRetentionExpiry: T_cut is now − retention.
func TestRetentionExpiry(t *testing.T) {
	t.Parallel()
	db, _ := open(t)
	now := int64(10 * dayMs)
	js, err := New(db, &Options{RetentionDays: 1, Now: func() int64 { return now }})
	if err != nil {
		t.Fatal(err)
	}
	r := writeRecord()
	first := record(t, js, r)
	now += dayMs
	r.Before, r.After = &journal.Snapshot{Data: []byte("x"), Mode: 0o600}, &journal.Snapshot{Data: []byte("y"), Mode: 0o600}
	second := record(t, js, r)
	now += 2 * dayMs // both are older than now−1 day
	for _, id := range []int64{first, second} {
		a, gerr := js.Get(t.Context(), id)
		if gerr != nil || !a.Expired {
			t.Fatalf("action %d expired = %v, %v", id, a.Expired, gerr)
		}
	}
}

// TestRetentionByteCap: the byte cap moves T_cut forward but the newest
// action always survives, even when it alone exceeds max_bytes (JN2-8).
func TestRetentionByteCap(t *testing.T) {
	t.Parallel()
	db, _ := open(t)
	now := int64(10 * dayMs)
	js, err := New(db, &Options{RetentionDays: 30, Now: func() int64 { return now }})
	if err != nil {
		t.Fatal(err)
	}
	first := record(t, js, writeRecord()) // 8 bytes
	now += dayMs
	r := writeRecord()
	r.Before, r.After = &journal.Snapshot{Data: []byte("x"), Mode: 0o600}, &journal.Snapshot{Data: []byte("y"), Mode: 0o600}
	second := record(t, js, r) // 2 bytes

	cap2, err := New(db, &Options{RetentionDays: 30, MaxBytes: 2, Now: func() int64 { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if a, gerr := cap2.Get(t.Context(), first); gerr != nil || !a.Expired {
		t.Fatalf("first expired by cap = %v, %v", a.Expired, gerr)
	}
	if a, gerr := cap2.Get(t.Context(), second); gerr != nil || a.Expired {
		t.Fatalf("second expired by cap = %v, %v", a.Expired, gerr)
	}
	cap1, err := New(db, &Options{RetentionDays: 30, MaxBytes: 1, Now: func() int64 { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if a, gerr := cap1.Get(t.Context(), second); gerr != nil || a.Expired {
		t.Fatalf("newest over cap expired = %v, %v", a.Expired, gerr)
	}
}

// TestValidateRejects: a record without a file inverse never reaches SQL.
func TestValidateRejects(t *testing.T) {
	t.Parallel()
	_, js := open(t)
	r := writeRecord()
	for name, mod := range map[string]func(*journal.Record){
		"kind":   func(r *journal.Record) { r.Kind = "shell" },
		"path":   func(r *journal.Record) { r.Path = "relative" },
		"move":   func(r *journal.Record) { r.Kind, r.MoveTo = journal.FileMove, "" },
		"images": func(r *journal.Record) { r.Before, r.After = nil, nil },
	} {
		rr := *r
		mod(&rr)
		if _, err := js.Record(t.Context(), &rr); err == nil {
			t.Errorf("%s: record accepted", name)
		}
	}
}

// TestAppendOnly: the actions table refuses UPDATE and DELETE (P9).
func TestAppendOnly(t *testing.T) {
	t.Parallel()
	db, js := open(t)
	id := record(t, js, writeRecord())
	for _, q := range []string{"UPDATE actions SET subject = 'x' WHERE id = ?", "DELETE FROM actions WHERE id = ?"} {
		err := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
			if _, xerr := tx.Exec(q, id); xerr != nil {
				return fmt.Errorf("append-only probe: %w", xerr)
			}
			return nil
		})
		if err == nil {
			t.Fatalf("%q succeeded", q)
		}
	}
}

// TestBlobRejectsBadSHA: a malformed or missing sha never reaches
// blobName's two-byte slice (JN2-7) and is reported as ErrBlob.
func TestBlobRejectsBadSHA(t *testing.T) {
	t.Parallel()
	_, js := open(t)
	for _, sha := range []string{"", "abc", strings.Repeat("z", 64), strings.Repeat("0", 64)} {
		if _, err := js.Blob(t.Context(), sha); !errors.Is(err, journal.ErrBlob) {
			t.Errorf("Blob(%q) = %v, want ErrBlob", sha, err)
		}
	}
}

// TestOpenTurnRefuses: while the turn journal row is open the action is
// reported Open, the signal undo uses to refuse.
func TestOpenTurnRefuses(t *testing.T) {
	t.Parallel()
	db, js := open(t)
	id := record(t, js, writeRecord())
	a, err := js.Get(t.Context(), id)
	if err != nil || a.Open {
		t.Fatalf("closed turn: open = %v, %v", a.Open, err)
	}
	if werr := db.Write(t.Context(), func(tx *ctxdb.Tx) error { return tx.StartTurn("s1", "t1") }); werr != nil {
		t.Fatal(werr)
	}
	a, err = js.Get(t.Context(), id)
	if err != nil || !a.Open {
		t.Fatalf("open turn: open = %v, %v", a.Open, err)
	}
}
