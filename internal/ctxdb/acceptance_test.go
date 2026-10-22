package ctxdb

import (
	"context"
	"testing"
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

// TestFTSAutoUpgrade (GAP-INF-02): an older fts_meta version is re-indexed
// automatically on open, without a manual command.
func TestFTSAutoUpgrade(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(context.Background(), &Options{Dir: dir, Kind: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	session(t, s, "S", "")
	appendMsg(t, s, "S", "auto upgrade token")
	if werr := s.Write(context.Background(), func(tx *Tx) error {
		_, xerr := tx.Exec(`UPDATE fts_meta SET norm_version=0, layout_version=0 WHERE name='events_fts'`)
		return xerr
	}); werr != nil {
		t.Fatal(werr)
	}
	if cerr := s.clearFTS(context.Background(), "events_fts"); cerr != nil {
		t.Fatal(cerr)
	}
	must(t, s.Close())
	s, err = Open(context.Background(), &Options{Dir: dir, Kind: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { must(t, s.Close()) }()
	hits, err := s.Recall(context.Background(), "auto upgrade", 5)
	if err != nil || len(hits) != 1 {
		t.Fatalf("after auto-upgrade recall = %+v, %v", hits, err)
	}
	var version int
	must(t, s.get(context.Background(), `SELECT norm_version FROM fts_meta WHERE name='events_fts'`, nil, &version))
	if version != ftsNormVersion {
		t.Fatalf("fts_meta version = %d, want %d", version, ftsNormVersion)
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
