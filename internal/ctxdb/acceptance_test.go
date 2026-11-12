package ctxdb

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestRecallCJKOrder (GAP-INF-01): bigram normalization is deterministic and
// bm25 puts the event with more matching CJK bigrams first; one-rune CJK
// queries use prefix matching, never LIKE.
func TestRecallCJKOrder(t *testing.T) {
	s, err := Open(context.Background(), &Options{Dir: t.TempDir(), Kind: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { must(t, s.Close()) }()
	ctx := context.Background()
	session(t, s, "S", "")
	ids := []int64{
		appendMsg(t, s, "S", "天气"),
		appendMsg(t, s, "S", "天气 天气"),
		appendMsg(t, s, "S", "天气 天气 天气"),
	}
	got, err := s.Recall(ctx, "天气", 3)
	if err != nil || len(got) != 3 {
		t.Fatalf("CJK recall = %+v, %v", got, err)
	}
	if got[0].EventID != ids[2] {
		t.Fatalf("CJK bm25 order = %+v, want event %d first", got, ids[2])
	}
	one, err := s.Recall(ctx, "天", 3)
	if err != nil || len(one) != 3 {
		t.Fatalf("one-rune CJK prefix = %+v, %v", one, err)
	}
}

// ftsStatusOf returns one fts_meta row without racing the background worker.
func ftsStatusOf(t *testing.T, s *Store) FTSStatus {
	t.Helper()
	var st FTSStatus
	must(t, s.get(context.Background(), `SELECT name, norm_version, layout_version, reindexed_to, total, status
		FROM fts_meta WHERE name='events_fts'`, nil,
		&st.Name, &st.NormVersion, &st.LayoutVersion, &st.ReindexedTo, &st.Total, &st.Status))
	return st
}

// TestFTSAutoUpgrade (GAP-INF-02): an older fts_meta version is re-indexed
// automatically on open, without a manual command; a kill -9 after one
// committed batch resumes from the persisted cursor with 0 duplicates.
func TestFTSAutoUpgrade(t *testing.T) {
	t.Run("upgrade", testFTSUpgrade)
	t.Run("resume_after_crash", testFTSResume)
}

func testFTSUpgrade(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(context.Background(), &Options{Dir: dir, Kind: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	session(t, s, "S", "")
	appendMsg(t, s, "S", "auto upgrade token")
	must(t, s.Write(ctx, func(tx *Tx) error {
		_, xerr := tx.Exec(`UPDATE fts_meta SET norm_version=0, layout_version=0`)
		return xerr
	}))
	must(t, s.clearFTS(ctx, "events_fts"))
	must(t, s.Close())

	s, err = Open(ctx, &Options{Dir: dir, Kind: "cli", FTSBatch: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { must(t, s.Close()) }()
	must(t, s.AwaitFTS(ctx))
	st := ftsStatusOf(t, s)
	if st.Status != "done" || st.NormVersion != ftsNormVersion || st.LayoutVersion != ftsPlainLayout || st.ReindexedTo == 0 {
		t.Fatalf("fts_meta after upgrade = %+v", st)
	}
	hits, err := s.Recall(ctx, "auto upgrade", 5)
	if err != nil || len(hits) != 1 || hits[0].Partial {
		t.Fatalf("after auto-upgrade recall = %+v, %v", hits, err)
	}
}

func testFTSResume(t *testing.T) {
	dir := t.TempDir()
	s := ftsResumeSeed(t, dir)
	must(t, s.Close())
	cmd := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^TestFTSUpgradeCrashHelper$") // #nosec G204,G702 -- os.Args[0] is this test binary; the argument is a fixed test name
	cmd.Env = append(os.Environ(),
		"DAMNBOT_FTS_CRASH_HELPER=1",
		"DAMNBOT_FTS_CRASH_DIR="+dir,
	)
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || (ee.ExitCode() != 9 && ee.ExitCode() != -1) {
		t.Fatalf("crash helper = %v, want exit 9/SIGKILL\n%s", err, out)
	}

	first := make(chan FTSProgress, 8)
	s, err = Open(context.Background(), &Options{
		Dir: dir, Kind: "cli", FTSBatch: 2,
		OnFTSProgress: func(p FTSProgress) { first <- p },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { must(t, s.Close()) }()
	must(t, s.AwaitFTS(context.Background()))
	select {
	case p := <-first:
		if p.Name != "events_fts" || p.Status != "partial" || p.Cursor <= 2 {
			t.Fatalf("first resumed batch = %+v, want cursor > 2", p)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("resume produced no progress")
	}
	st := ftsStatusOf(t, s)
	if st.Status != "done" {
		t.Fatalf("fts_meta after resume = %+v", st)
	}
	ids, err := s.ints(context.Background(), `SELECT rowid FROM events_fts WHERE events_fts MATCH ?`, "resume")
	if err != nil || len(ids) != 5 {
		t.Fatalf("resume match rows = %d, %v; want 5 (0 duplicates)", len(ids), err)
	}
}

// TestFTSUpgradeCrashHelper is the child process of resume_after_crash: it
// commits one 2-row batch and exits without Close, like kill -9.
func TestFTSUpgradeCrashHelper(t *testing.T) {
	if os.Getenv("DAMNBOT_FTS_CRASH_HELPER") == "" {
		t.Skip("helper process only")
	}
	dir := os.Getenv("DAMNBOT_FTS_CRASH_DIR")
	_, err := Open(context.Background(), &Options{
		Dir: dir, Kind: "cli", FTSBatch: 2,
		OnFTSProgress: func(p FTSProgress) {
			if p.Name == "events_fts" && p.Status == "partial" {
				crashSelf()
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	<-time.After(10 * time.Second)
	t.Fatal("no re-index started")
}

// ftsResumeSeed writes five events and leaves events_fts empty + partial so
// the child re-index always has two batches.
func ftsResumeSeed(t *testing.T, dir string) *Store {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, &Options{Dir: dir, Kind: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	session(t, s, "S", "")
	for range 5 {
		appendMsg(t, s, "S", "resume token")
	}
	must(t, s.Write(ctx, func(tx *Tx) error {
		_, xerr := tx.Exec(`UPDATE fts_meta SET norm_version=0, layout_version=0`)
		return xerr
	}))
	must(t, s.clearFTS(ctx, "events_fts"))
	return s
}

// TestFTSReindexPartial: searches keep answering while the background upgrade
// runs, every hit is marked partial, and the marker clears at 'done'.
func TestFTSReindexPartial(t *testing.T) { //nolint:gocyclo // one script checks partial state, recall marker, and done
	dir := t.TempDir()
	ctx := context.Background()
	s, err := Open(ctx, &Options{Dir: dir, Kind: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	session(t, s, "S", "")
	for range 3 {
		appendMsg(t, s, "S", "partial marker")
	}
	must(t, s.Write(ctx, func(tx *Tx) error {
		_, xerr := tx.Exec(`UPDATE fts_meta SET norm_version=0 WHERE name='events_fts'`)
		return xerr
	}))
	must(t, s.clearFTS(ctx, "events_fts"))
	must(t, s.Close())

	paused := make(chan struct{}, 1)
	release := make(chan struct{})
	s, err = Open(ctx, &Options{
		Dir: dir, Kind: "cli", FTSBatch: 2,
		OnFTSProgress: func(p FTSProgress) {
			if p.Name == "events_fts" && p.Status == "partial" {
				paused <- struct{}{}
				<-release
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { must(t, s.Close()) }()
	select {
	case <-paused:
	case <-time.After(10 * time.Second):
		t.Fatal("background re-index did not commit a batch")
	}
	st := ftsStatusOf(t, s)
	if st.Status != "partial" || st.ReindexedTo != 2 {
		t.Fatalf("partial state = %+v, want cursor 2", st)
	}
	hits, err := s.Recall(ctx, "partial marker", 10)
	if err != nil || len(hits) != 2 || !hits[0].Partial {
		t.Fatalf("partial recall = %+v, %v; want 2 partial hits", hits, err)
	}
	close(release)
	must(t, s.AwaitFTS(ctx))
	if st = ftsStatusOf(t, s); st.Status != "done" {
		t.Fatalf("final state = %+v", st)
	}
	hits, err = s.Recall(ctx, "partial marker", 10)
	if err != nil || len(hits) != 3 || hits[0].Partial {
		t.Fatalf("done recall = %+v, %v; want 3 non-partial hits", hits, err)
	}
}

// TestFTSMetaMigration: a database created at the previous ladder version
// opens, migrates, and keeps its searchable index (no manual reindex).
func TestFTSMetaMigration(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	migs, err := migrations()
	must(t, err)
	old := len(migs) - 1
	s, err := Open(ctx, &Options{Dir: dir, Kind: "cli", SchemaMax: old})
	if err != nil {
		t.Fatal(err)
	}
	session(t, s, "S", "")
	appendMsg(t, s, "S", "legacy schema token")
	must(t, s.Close())

	s, err = Open(ctx, &Options{Dir: dir, Kind: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { must(t, s.Close()) }()
	must(t, s.AwaitFTS(ctx))
	var version int
	must(t, s.get(ctx, `PRAGMA user_version`, nil, &version))
	if version != len(migs) {
		t.Fatalf("user_version = %d, want %d", version, len(migs))
	}
	hits, err := s.Recall(ctx, "legacy schema", 5)
	if err != nil || len(hits) != 1 {
		t.Fatalf("recall after migration = %+v, %v", hits, err)
	}
	if got := ftsStatusOf(t, s); got.Status != "done" {
		t.Fatalf("migrated fts_meta = %+v", got)
	}
}

// TestFTSBlobIndex: tool output blobs are indexed in blob_fts by event id.
func TestFTSBlobIndex(t *testing.T) {
	s, err := Open(context.Background(), &Options{Dir: t.TempDir(), Kind: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { must(t, s.Close()) }()
	ctx := context.Background()
	session(t, s, "S", "")
	pb, err := s.PutBlob([]byte("full tool output canary"), "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	ids, err := s.Append(ctx, "S", []*PendingBlob{pb}, Event{Kind: "tool_result", Role: "tool", Blob: pb.Sha})
	if err != nil {
		t.Fatal(err)
	}
	if rerr := s.ReindexFTS(ctx, 1); rerr != nil {
		t.Fatal(rerr)
	}
	got, err := s.ints(ctx, `SELECT rowid FROM blob_fts WHERE blob_fts MATCH ?`, "canary")
	if err != nil || len(got) != 1 || got[0] != ids[0] {
		t.Fatalf("blob_fts = %v, %v", got, err)
	}
}

// TestFTSBlobContentless: blob_fts is a contentless index (content=”,
// contentless_delete=1) keyed by the owning event id.
func TestFTSBlobContentless(t *testing.T) {
	s, err := Open(context.Background(), &Options{Dir: t.TempDir(), Kind: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { must(t, s.Close()) }()
	var ddl string
	must(t, s.get(context.Background(), `SELECT sql FROM sqlite_master WHERE name='blob_fts'`, nil, &ddl))
	compact := strings.ReplaceAll(ddl, " ", "")
	for _, want := range []string{"content=''", "contentless_delete=1"} {
		if !strings.Contains(compact, want) {
			t.Fatalf("blob_fts DDL lacks %s: %s", want, ddl)
		}
	}
}

// TestFTSBlobMaxBytes: exactly 16 MiB is indexed; one byte over is skipped
// with the documented cap reason (GAP-INF-02).
func TestFTSBlobMaxBytes(t *testing.T) {
	s, err := Open(context.Background(), &Options{Dir: t.TempDir(), Kind: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { must(t, s.Close()) }()
	ctx := context.Background()
	session(t, s, "S", "")
	pad := func(n int, canary string) []byte {
		return append(bytes.Repeat([]byte(" "), n-len(canary)), canary...)
	}
	for _, tc := range []struct {
		name, canary string
		data         []byte
		want         bool
	}{
		{"at-cap", "boundarycanary", pad(ftsBlobMaxBytes, "boundarycanary"), true},
		{"over-cap", "overflowcanary", pad(ftsBlobMaxBytes+1, "overflowcanary"), false},
	} {
		pb, perr := s.PutBlob(tc.data, "text/plain")
		if perr != nil {
			t.Fatal(perr)
		}
		ids, aerr := s.Append(ctx, "S", []*PendingBlob{pb}, Event{Kind: "tool_result", Role: "tool", Blob: pb.Sha})
		if aerr != nil {
			t.Fatal(aerr)
		}
		got, qerr := s.ints(ctx, `SELECT rowid FROM blob_fts WHERE blob_fts MATCH ?`, tc.canary)
		if qerr != nil {
			t.Fatalf("%s match: %v", tc.name, qerr)
		}
		if tc.want && (len(got) != 1 || got[0] != ids[0]) {
			t.Fatalf("%s = %v, want event %d", tc.name, got, ids[0])
		}
		if !tc.want && len(got) != 0 {
			t.Fatalf("%s = %v, want unindexed (>16 MiB cap)", tc.name, got)
		}
	}
}

// TestRecallToolOutputUsesMatch is the negative control: the tool_result
// branch is MATCH over blob_fts, never a LIKE scan (GAP-INF-02).
func TestRecallToolOutputUsesMatch(t *testing.T) {
	if !strings.Contains(recallBlobSQL(), "blob_fts MATCH") {
		t.Fatalf("recall blob branch is not MATCH: %s", recallBlobSQL())
	}
	if strings.Contains(strings.ToUpper(recallBlobSQL()), "LIKE") {
		t.Fatalf("recall blob branch still uses LIKE: %s", recallBlobSQL())
	}
}

// TestRecallToolOutputBlob (GAP-INF-02): output spilled by turn 1 is found
// later through Recall, not via LIKE.
func TestRecallToolOutputBlob(t *testing.T) {
	s, err := Open(context.Background(), &Options{Dir: t.TempDir(), Kind: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { must(t, s.Close()) }()
	ctx := context.Background()
	session(t, s, "S", "")
	pb, err := s.PutBlob([]byte("full tool output canary from turn one"), "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	if _, aerr := s.Append(ctx, "S", []*PendingBlob{pb}, Event{Kind: "tool_result", Role: "tool", Blob: pb.Sha}); aerr != nil {
		t.Fatal(aerr)
	}
	hits, err := s.Recall(ctx, "tool output canary", 5)
	if err != nil || len(hits) == 0 || hits[0].SessionID != "S" {
		t.Fatalf("tool output recall = %+v, %v", hits, err)
	}
}
