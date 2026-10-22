package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/config"
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

// TestSessionsRename: rename writes the title label and prints it back.
func TestSessionsRename(t *testing.T) {
	t.Parallel()
	dir, id := seedSession(t)
	if code, _, e := runAdmin(t, "rename", id, "My", "Title", "--data-dir", dir); code != 0 {
		t.Fatalf("rename = %d: %s", code, e)
	}
	code, out, e := runAdmin(t, "rename", id, "--data-dir", dir)
	if code != 0 || !strings.Contains(out, "title: My Title") {
		t.Fatalf("rename read = %d %q (%s)", code, out, e)
	}
}

// TestSessionsExportMarkdown: md export includes the title and transcript,
// redacts secrets by default and keeps them with --no-redact.
func TestSessionsExportMarkdown(t *testing.T) {
	t.Parallel()
	dir, id := seedSession(t)
	if code, _, e := runAdmin(t, "rename", id, "My", "Title", "--data-dir", dir); code != 0 {
		t.Fatalf("rename = %d: %s", code, e)
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
	if code, raw, _ := runAdmin(t, "export", id, "--format", "md", "--no-redact", "--data-dir", dir); code != 0 {
		t.Fatal("export --no-redact failed")
	} else if !strings.Contains(raw, "sk-abcdefghijklmnopqrstuvwxyz0123456789") {
		t.Errorf("--no-redact still redacted:\n%s", raw)
	}
	if code, _, _ := runAdmin(t, "export", id, "--format", "exe", "--data-dir", dir); code != 2 {
		t.Errorf("unknown format exit = %d, want 2", code)
	}
}

// TestSessionsExportJSONL: one JSON object per event.
func TestSessionsExportJSONL(t *testing.T) {
	t.Parallel()
	dir, id := seedSession(t)
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
}

// TestSessionsExportHTML: HTML export escapes transcript content.
func TestSessionsExportHTML(t *testing.T) {
	t.Parallel()
	dir, id := seedSession(t)
	code, page, e := runAdmin(t, "export", id, "--format", "html", "--data-dir", dir)
	if code != 0 {
		t.Fatalf("export html = %d: %s", code, e)
	}
	if strings.Contains(page, "<script>alert(1)</script>") || !strings.Contains(page, "&lt;script&gt;") {
		t.Errorf("html export did not escape content:\n%s", page)
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
	if err != nil {
		t.Fatalf("stat exported file: %v", err)
	}
	// Windows has no POSIX permission bits: os.Stat reports 0666 for any
	// writable file, so a 0600 export is unobservable there (Chmod only toggles
	// the read-only attribute). The assertion below is the POSIX check.
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("export file mode = %v, want 0600", fi.Mode().Perm())
	}
}


// TestSessionsExportRefusesExisting: --out never truncates an existing file
// or writes through a symlink.
func TestSessionsExportRefusesExisting(t *testing.T) {
	t.Parallel()
	dir, id := seedSession(t)
	out := filepath.Join(t.TempDir(), "existing.md")
	if err := os.WriteFile(out, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, e := runAdmin(t, "export", id, "--out", out, "--data-dir", dir)
	if code != 1 || !strings.Contains(e, "refusing to overwrite") {
		t.Fatalf("existing --out = %d %q, want exit 1 refusal", code, e)
	}
	if b, err := os.ReadFile(out); err != nil || string(b) != "keep" { //nolint:gosec // test temp path
		t.Fatalf("existing file changed: %q (%v)", b, err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	link := filepath.Join(t.TempDir(), "link.md")
	if err := os.Symlink(out, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if code, _, e = runAdmin(t, "export", id, "--out", link, "--data-dir", dir); code != 1 || !strings.Contains(e, "refusing to overwrite") {
		t.Fatalf("symlink --out = %d %q, want exit 1 refusal", code, e)
	}
}

// TestSessionsExportRedactsMetadata: title/model/cwd are redacted like
// content, so a secret in a title never reaches the export.
func TestSessionsExportRedactsMetadata(t *testing.T) {
	t.Parallel()
	const secret = "sk-abcdefghijklmnopqrstuvwxyz0123456789"
	dir, id := seedSession(t)
	if code, _, e := runAdmin(t, "rename", id, secret, "--data-dir", dir); code != 0 {
		t.Fatalf("rename = %d: %s", code, e)
	}
	code, md, e := runAdmin(t, "export", id, "--data-dir", dir)
	if code != 0 {
		t.Fatalf("export = %d: %s", code, e)
	}
	if strings.Contains(md, secret) || !strings.Contains(md, "[REDACTED:") {
		t.Errorf("metadata secret not redacted:\n%s", md)
	}
}
// TestSessionsPrune: --archive labels old sessions; deletion needs --yes and
// vacuums afterwards.
func TestSessionsPrune(t *testing.T) {
	t.Parallel()
	dir, id := seedSession(t)
	if code, out, e := runAdmin(t, "prune", "--older-than", "1ns", "--archive", "--data-dir", dir); code != 0 || !strings.Contains(out, "archived 1") {
		t.Fatalf("prune --archive = %d %q (%s)", code, out, e)
	}
	if code, out, _ := runAdmin(t, "stats", "--data-dir", dir); !strings.Contains(out, "archived: 1") {
		t.Fatalf("stats after archive:\n%s\n(%d)", out, code)
	}
	if code, _, e := runAdmin(t, "prune", "--older-than", "1ns", "--data-dir", dir); code != 2 {
		t.Fatalf("prune without --yes = %d: %s", code, e)
	}
	code, out, e := runAdmin(t, "prune", "--older-than", "90d", "--dry-run", "--data-dir", dir)
	if code != 0 || !strings.Contains(out, "would purge 0") {
		t.Fatalf("dry-run 90d = %d %q (%s)", code, out, e)
	}
	code, out, e = runAdmin(t, "prune", "--older-than", "1ns", "--dry-run", "--data-dir", dir)
	if code != 0 || !strings.Contains(out, "would purge 1") || !strings.Contains(out, id) {
		t.Fatalf("dry-run 1ns = %d %q (%s)", code, out, e)
	}
	code, out, e = runAdmin(t, "prune", "--older-than", "1ns", "--yes", "--data-dir", dir)
	if code != 0 || !strings.Contains(out, "purged 1") {
		t.Fatalf("prune --yes = %d %q (%s)", code, out, e)
	}
	if code, out, _ := runAdmin(t, "stats", "--data-dir", dir); !strings.Contains(out, "sessions: 0") {
		t.Fatalf("stats after purge = %d:\n%s", code, out)
	}
	if code, _, _ := runAdmin(t, "prune", "--older-than", "x", "--data-dir", dir); code != 2 {
		t.Errorf("bad age exit = %d, want 2", code)
	}
}

func TestParseAge(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{"90d": "2160h0m0s", "24h": "24h0m0s"} {
		d, err := parseAge(in)
		if err != nil || d.String() != want {
			t.Errorf("parseAge(%q) = %v, %v; want %s", in, d, err, want)
		}
	}
	for _, bad := range []string{"", "0d", "-1d", "x"} {
		if _, err := parseAge(bad); err == nil {
			t.Errorf("parseAge(%q) accepted", bad)
		}
	}
}

// oldClock seeds sessions in the past so the auto-prune age is testable
// without waiting or mutating the immutable sessions table.
type oldClock struct{ wall, boot, mono int64 }

func (c oldClock) BootMs() int64                              { return c.boot }
func (c oldClock) MonoMs() int64                              { return c.mono }
func (c oldClock) WallMs() int64                              { return c.wall }
func (c oldClock) Sleep(context.Context, time.Duration) error { return nil }

// TestAutoPrune: the [sessions] auto-prune deletes sessions older than the
// configured days when enabled and does nothing when disabled.
func TestAutoPrune(t *testing.T) {
	t.Parallel()
	seedOld := func(t *testing.T, days int) (dir, id string) {
		t.Helper()
		dir = t.TempDir()
		ctx := context.Background()
		wall := time.Now().Add(-time.Duration(days) * 24 * time.Hour).UnixMilli()
		db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: dir, Kind: "cli", Clock: oldClock{wall: wall, boot: wall, mono: wall}})
		if err != nil {
			t.Fatal(err)
		}
		id = "OLD"
		if _, err := db.CreateSession(ctx, ctxdb.Session{ID: id, Origin: "headless", Cwd: "/w"}); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Append(ctx, id, nil, ctxdb.Event{Kind: "user_message", Role: "user", Content: "old"}); err != nil {
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
	open := func(t *testing.T, dir string) *ctxdb.Store {
		t.Helper()
		db, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: dir, Kind: "cli"})
		if err != nil {
			t.Fatal(err)
		}
		return db
	}
	t.Run("disabled", func(t *testing.T) {
		t.Parallel()
		dir, _ := seedOld(t, 100)
		db := open(t, dir)
		defer db.Close()
		var errb bytes.Buffer
		autoPrune(context.Background(), db, config.Sessions{AutoPrune: false, PruneAfterDays: 90}, &errb)
		rows, err := db.SessionRows(context.Background())
		if err != nil || len(rows) != 1 {
			t.Fatalf("rows = %d, %v; want 1", len(rows), err)
		}
		if errb.Len() != 0 {
			t.Errorf("disabled auto-prune wrote %q", errb.String())
		}
	})
	t.Run("enabled", func(t *testing.T) {
		t.Parallel()
		dir, _ := seedOld(t, 100)
		db := open(t, dir)
		defer db.Close()
		var errb bytes.Buffer
		autoPrune(context.Background(), db, config.Sessions{AutoPrune: true, PruneAfterDays: 90}, &errb)
		rows, err := db.SessionRows(context.Background())
		if err != nil || len(rows) != 0 {
			t.Fatalf("rows = %d, %v; want 0 after auto-prune (stderr %s)", len(rows), err, errb.String())
		}
		if !strings.Contains(errb.String(), "auto-pruned 1 sessions") {
			t.Errorf("auto-prune notice = %q", errb.String())
		}
	})
}
