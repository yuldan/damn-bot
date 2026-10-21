package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/codetools"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/journalstore"
	"github.com/damnbot/damnbot/internal/provider/providertest"
	"github.com/damnbot/damnbot/internal/tools"
)

// toolReply scripts one fake-provider turn that calls name(args).
func toolReply(t *testing.T, id, name string, args any) providertest.Reply {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	call, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{
		"tool_calls": []any{map[string]any{"index": 0, "id": id, "function": map[string]any{"name": name, "arguments": string(raw)}}},
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	return providertest.Reply{Frames: []string{
		string(call),
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":50,"completion_tokens":10}}`,
		"[DONE]",
	}}
}

// tagOf is the codetools tag of small content: sha256 prefix (CODE-TOOLS §5.1).
func tagOf(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:4]) }

// undoWorkspace chdirs into a fresh workspace holding f.txt (mode) and runs
// one headless turn whose model reads f.txt and edits "beta" → "gamma".
func undoWorkspace(t *testing.T, orig []byte, mode os.FileMode) (ws, data string) {
	t.Helper()
	ws, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data = t.TempDir()
	if err = os.WriteFile(filepath.Join(ws, "f.txt"), orig, mode); err != nil {
		t.Fatal(err)
	}
	t.Chdir(ws)
	edit := map[string]any{
		"path": "f.txt", "tag": tagOf(orig),
		"edits": []any{map[string]any{"old_string": "beta", "new_string": "gamma"}},
	}
	srv := providertest.New(t, nil,
		toolReply(t, "c1", "read_file", map[string]any{"path": "f.txt"}),
		toolReply(t, "c2", "edit_file", edit),
		providertest.Text("edited"),
	)
	var out, errb bytes.Buffer
	code := run([]string{
		"run", "-p", "edit f.txt", "--model", "openai/gpt-fake", "--base-url", srv.URL,
		"--data-dir", data, "--output", "json", "--approval", "auto-edit",
	}, &out, &errb)
	if code != 0 {
		t.Fatalf("run exit = %d, stderr = %s, stdout = %s", code, errb.String(), out.String())
	}
	if got := readFile(t, filepath.Join(ws, "f.txt")); !bytes.Contains(got, []byte("gamma")) {
		t.Fatalf("edit not applied: %q (stdout %s)", got, out.String())
	}
	return ws, data
}

func readFile(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p) //nolint:gosec // G304: test temp path
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func realDir(t testing.TB) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// openData opens the data dir as the test's own ctxdb holder.
func openData(t testing.TB, data string) *ctxdb.Store {
	t.Helper()
	db, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: data, Kind: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Error(cerr)
		}
	})
	return db
}

// TestRunStartFailureReleasesLease (REVIEW-F2-SEC5-5): a run that fails
// after creating its session leaves no lease behind.
func TestRunStartFailureReleasesLease(t *testing.T) {
	t.Parallel()
	data := t.TempDir()
	var out, errb bytes.Buffer
	if code := run([]string{"run", "-p", "hi", "--model", "nobase/m", "--data-dir", data}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "needs --base-url") {
		t.Fatalf("exit %d stderr %q", code, errb.String())
	}
	var sessions, leases int64
	err := openData(t, data).Write(context.Background(), func(tx *ctxdb.Tx) error {
		if gerr := tx.Get("SELECT count(*) FROM sessions WHERE id <> '__system__'", nil, &sessions); gerr != nil {
			return gerr //nolint:wrapcheck // test query
		}
		return tx.Get("SELECT count(*) FROM session_leases", nil, &leases)
	})
	if err != nil || sessions != 1 || leases != 0 {
		t.Fatalf("sessions = %d, leases = %d, err = %v", sessions, leases, err)
	}
}

// BenchmarkJournalRecordEdit1MB measures the production recorder on a 1 MB
// edit (REVIEW-F2-SEC5-10): both images read, blobs published and the
// actions row written through journalstore on a real ctxdb.
func BenchmarkJournalRecordEdit1MB(b *testing.B) {
	ctx := context.Background()
	dir := realDir(b)
	db := openData(b, b.TempDir())
	if _, err := db.CreateSession(ctx, ctxdb.Session{ID: "B", Origin: "headless", Cwd: dir}); err != nil {
		b.Fatal(err)
	}
	store, err := journalstore.New(db, nil)
	if err != nil {
		b.Fatal(err)
	}
	scope, err := journalScope([]string{dir}, nil, "")
	if err != nil {
		b.Fatal(err)
	}
	rec := &fileJournal{store: store, sid: "B", scope: scope}
	body := bytes.Repeat([]byte(strings.Repeat("e", 99)+"\n"), 10<<10)
	before, after := tempWith(b, dir, "before", body), tempWith(b, dir, "after", body)
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		if _, werr := after.WriteAt([]byte(strconv.Itoa(i)), 0); werr != nil { // distinct blob per op
			b.Fatal(werr)
		}
		c := &codetools.Commit{Path: filepath.Join(dir, "e.txt"), Before: before, After: after, Mode: 0o600}
		if rerr := rec.record(ctx, tools.Env{CallID: "c" + strconv.Itoa(i)}, "edit_file", c); rerr != nil {
			b.Fatal(rerr)
		}
	}
}

func tempWith(b *testing.B, dir, name string, data []byte) *os.File {
	b.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		b.Fatal(err)
	}
	f, err := os.OpenFile(p, os.O_RDWR, 0) //nolint:gosec // G304: benchmark temp file
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if cerr := f.Close(); cerr != nil {
			b.Error(cerr)
		}
	})
	return f
}

// TestOneDispatchPerCall (REVIEW-F2-SEC5-9): a read + edit turn has exactly
// one tool_dispatch and one tool_result per call, and the edit's action row
// hangs on the edit call's dispatch event with its turn id.
func TestOneDispatchPerCall(t *testing.T) {
	_, data := undoWorkspace(t, []byte("alpha\nbeta\n"), 0o600)
	var dispatches, results, anchored int64
	err := openData(t, data).Write(context.Background(), func(tx *ctxdb.Tx) error {
		for q, dst := range map[string]*int64{
			"SELECT count(*) FROM events WHERE kind = 'tool_dispatch'": &dispatches,
			"SELECT count(*) FROM events WHERE kind = 'tool_result'":   &results,
			`SELECT count(*) FROM actions a JOIN events e ON e.id = a.event_id
				WHERE e.kind = 'tool_dispatch' AND e.tool_call_id = 'c2' AND a.turn_id = e.turn_id AND a.turn_id <> ''`: &anchored,
		} {
			if gerr := tx.Get(q, nil, dst); gerr != nil {
				return gerr //nolint:wrapcheck // test query
			}
		}
		return nil
	})
	if err != nil || dispatches != 2 || results != 2 || anchored != 1 {
		t.Fatalf("tool_dispatch = %d, tool_result = %d, anchored actions = %d, err = %v", dispatches, results, anchored, err)
	}
}
