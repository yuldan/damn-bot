package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// seedSession creates one closed data dir with a two-event session.
func seedSession(t *testing.T) (dir, id string) {
	t.Helper()
	dir = t.TempDir()
	ctx := context.Background()
	db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: dir, Kind: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	id = "S1"
	if _, err := db.CreateSession(ctx, ctxdb.Session{ID: id, Origin: "headless", Cwd: "/w", Model: "openai/m"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Append(ctx, id, nil,
		ctxdb.Event{Kind: "user_message", Role: "user", Content: "hello <script>alert(1)</script>"},
		ctxdb.Event{Kind: "assistant_message", Role: "assistant", Content: "token sk-abcdefghijklmnopqrstuvwxyz0123456789"}); err != nil {
		t.Fatal(err)
	}
	if err := db.ReleaseLease(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return dir, id
}

func runAdmin(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = runSessions(args, &out, &errb)
	return code, out.String(), errb.String()
}

// TestSessionsRenameAndExport: rename writes the title label; export renders
// md/jsonl/html, redacts secrets by default and escapes HTML.
func TestSessionsRenameAndExport(t *testing.T) {
	t.Parallel()
	dir, id := seedSession(t)
	if code, _, e := runAdmin(t, "rename", id, "My", "Title", "--data-dir", dir); code != 0 {
		t.Fatalf("rename = %d: %s", code, e)
	}
	code, out, e := runAdmin(t, "rename", id, "--data-dir", dir)
	if code != 0 || !strings.Contains(out, "title: My Title") {
		t.Fatalf("rename read = %d %q (%s)", code, out, e)
	}
	code, md, e := runAdmin(t, "export", id, "--data-dir", dir)
	if code != 0 {
		t.Fatalf("export md = %d: %s", code, e)
	}
	if !strings.HasPrefix(md, "# My Title\n") || !strings.Contains(md, "hello") {
		t.Fatalf("markdown export:\n%s", md)
	}
	if strings.Contains(md, "sk-abcdefghijklmnopqrstuvwxyz0123456789") {
		t.Errorf("export leaked the secret:\n%s", md)
	}
	if !strings.Contains(md, "[REDACTED:") {
		t.Errorf("export did not mark the redaction:\n%s", md)
	}
	// --no-redact keeps the raw token.
	if code, md, _ = runAdmin(t, "export", id, "--format", "md", "--no-redact", "--data-dir", dir); code != 0 {
		t.Fatal("export --no-redact failed")
	}
	if !strings.Contains(md, "sk-abcdefghijklmnopqrstuvwxyz0123456789") {
		t.Errorf("--no-redact still redacted:\n%s", md)
	}
	code, jsonl, e := runAdmin(t, "export", id, "--format", "jsonl", "--data-dir", dir)
	if code != 0 {
		t.Fatalf("export jsonl = %d: %s", code, e)
	}
	lines := strings.Split(strings.TrimSpace(jsonl), "\n")
	if len(lines) != 2 {
		t.Fatalf("jsonl lines = %d, want 2:\n%s", len(lines), jsonl)
	}
	var row map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &row); err != nil {
		t.Fatalf("jsonl line: %v", err)
	}
	if row["role"] != "user" || row["seq"] == nil {
		t.Errorf("jsonl row = %v", row)
	}
	code, page, e := runAdmin(t, "export", id, "--format", "html", "--data-dir", dir)
	if code != 0 {
		t.Fatalf("export html = %d: %s", code, e)
	}
	if strings.Contains(page, "<script>alert(1)</script>") || !strings.Contains(page, "&lt;script&gt;") {
		t.Errorf("html export did not escape content:\n%s", page)
	}
	if code, _, _ = runAdmin(t, "export", id, "--format", "exe", "--data-dir", dir); code != 2 {
		t.Errorf("unknown format exit = %d, want 2", code)
	}
}

// TestSessionsExportFile: --out writes a 0600 file and leaves stdout empty.
func TestSessionsExportFile(t *testing.T) {
	t.Parallel()
	dir, id := seedSession(t)
	out := filepath.Join(t.TempDir(), "session.md")
	code, stdout, e := runAdmin(t, "export", id, "--out", out, "--data-dir", dir)
	if code != 0 || stdout != "" {
		t.Fatalf("export --out = %d stdout %q (%s)", code, stdout, e)
	}
	b, err := os.ReadFile(out) //nolint:gosec // test temp path
	if err != nil || !strings.Contains(string(b), "hello") {
		t.Fatalf("exported file: %v\n%s", err, b)
	}
	fi, err := os.Stat(out)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("export file mode = %v, want 0600", fi.Mode().Perm())
	}
}

// TestSessionsStatsAndFlags: pin/unpin/archive drive the stats counters.
func TestSessionsStatsAndFlags(t *testing.T) {
	t.Parallel()
	dir, id := seedSession(t)
	if code, _, e := runAdmin(t, "pin", id, "--data-dir", dir); code != 0 {
		t.Fatalf("pin = %d: %s", code, e)
	}
	if code, out, _ := runAdmin(t, "pinned", "--data-dir", dir); code != 0 || !strings.Contains(out, id) {
		t.Fatalf("pinned = %d %q", code, out)
	}
	if code, out, _ := runAdmin(t, "stats", "--data-dir", dir); code != 0 || !strings.Contains(out, "pinned: 1") || !strings.Contains(out, "events: 2") {
		t.Fatalf("stats = %d:\n%s", code, out)
	}
	if code, _, e := runAdmin(t, "archive", id, "--data-dir", dir); code != 0 {
		t.Fatalf("archive = %d: %s", code, e)
	}
	if code, out, _ := runAdmin(t, "stats", "--data-dir", dir); code != 0 || !strings.Contains(out, "archived: 1") {
		t.Fatalf("stats after archive = %d:\n%s", code, out)
	}
	if code, _, e := runAdmin(t, "unpin", id, "--data-dir", dir); code != 0 {
		t.Fatalf("unpin = %d: %s", code, e)
	}
	if code, _, _ := runAdmin(t, "pin", "--data-dir", dir); code != 2 {
		t.Errorf("pin without id exit = %d, want 2", code)
	}
}
