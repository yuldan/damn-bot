package codetools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

// TestEditLimits is CT-33: ≤ 50 edits per call, diff ≤ 60 lines,
// in-memory string edits ≤ 64 MiB (above: too_large), ≤ 3 candidates.
func TestEditLimits(t *testing.T) {
	t.Parallel()
	v := newEnv(t)
	v.put(t, "f.txt", numbered(200))
	tag := v.tag(t, "f.txt")
	ctx := context.Background()
	edits := make([]EditItem, maxEdits+1)
	for i := range edits {
		edits[i] = EditItem{OldString: new(fmt.Sprintf("line %d\n", i+1)), NewString: new("x\n")}
	}
	_, err := v.e.Edit(ctx, EditArgs{Path: "f.txt", Tag: tag, Edits: edits}, Opts{})
	wantCode(t, err, CodeInvalidArgs)
	r, err := v.e.Edit(ctx, EditArgs{Path: "f.txt", Tag: tag, Edits: edits[:maxEdits]}, Opts{})
	if err != nil {
		t.Fatal(err)
	}
	if rows := strings.Count(r.Content, "\n"); rows > diffMaxLines+2 || !strings.Contains(r.Content, "more diff lines") {
		t.Errorf("diff has %d rows: %q", rows, r.Content[len(r.Content)-80:])
	}
	if r.Meta["diff_stat"] != "+50 -50" || r.Meta["replacements"] != maxEdits {
		t.Errorf("meta %v", r.Meta)
	}
	v.put(t, "c.txt", strings.Repeat("\tfoo()\n\tbar()\n", 6))
	_, err = v.e.Edit(ctx, EditArgs{Path: "c.txt", Tag: v.tag(t, "c.txt"), Edits: []EditItem{{OldString: new("    foo()\n    bar()"), NewString: new("z")}}}, Opts{})
	wantCode(t, err, CodeNoMatch)
	if hint := asToolErr(t, err).Hint; strings.Count(hint, "whitespace differs") != maxCandidates || !strings.HasPrefix(hint, "nearest (NOT applied): L1-2 ") {
		t.Errorf("candidates %q", hint)
	}
}

// TestEditFormat checks the CODE-TOOLS §9.4/§9.5 shapes.
func TestEditFormat(t *testing.T) {
	t.Parallel()
	v := newEnv(t)
	v.put(t, "b.go", "a\nb\n\tif err := f.Sync(); err != nil {\n\t\treturn err\n\t}\nc\nd\n")
	tag := v.tag(t, "b.go")
	r, err := v.e.Edit(context.Background(), EditArgs{Path: "b.go", Tag: tag, Edits: []EditItem{{
		OldString: new("\t\treturn err\n"), NewString: new("\t\treturn fmt.Errorf(\"x: %w\", err)\n\t}\n\tif err := d.Sync(); err != nil {\n"),
	}}}, Opts{})
	if err != nil {
		t.Fatal(err)
	}
	newTag := metaStr(&r, "tag")
	want := fmt.Sprintf("edited b.go #%s→#%s · 1 edit · +3 -1\n@@ -2,5 +2,7 @@\n b\n \tif err := f.Sync(); err != nil {\n-\t\treturn err\n+\t\treturn fmt.Errorf(\"x: %%w\", err)\n+\t}\n+\tif err := d.Sync(); err != nil {\n \t}\n c", tag, newTag)
	if r.Content != want {
		t.Errorf("got\n%s\nwant\n%s", r.Content, want)
	}
	_, err = v.e.Edit(context.Background(), EditArgs{Path: "b.go", Tag: newTag, Edits: []EditItem{{OldString: new("\t}\n"), NewString: new("}")}}}, Opts{})
	if codeOf(err) != CodeAmbiguousMatch || asToolErr(t, err).Message != "edit 1: 2 matches at L5, L7" {
		t.Errorf("ambiguous: %v", err)
	}
}

// TestWriteReadBack is CT-20/TOOL-03: every write is read back and its
// sha256 compared; new files get files.new_eol, no BOM, parents created.
func TestWriteReadBack(t *testing.T) {
	t.Parallel()
	v := newEnv(t)
	ctx := context.Background()
	r, err := v.e.Write(ctx, WriteArgs{Path: "deep/er/new.txt", Content: "a\nb\n"}, Opts{})
	if err != nil {
		t.Fatal(err)
	}
	if got := v.get(t, "deep/er/new.txt"); got != "a\nb\n" || r.Meta["created"] != true || !strings.HasPrefix(r.Content, "created deep/er/new.txt #") {
		t.Errorf("create: %q %v", got, r)
	}
	_, err = v.e.Write(ctx, WriteArgs{Path: "deep/er/new.txt", Content: "x"}, Opts{})
	wantCode(t, err, CodeStaleRead)
	_, err = v.e.Write(ctx, WriteArgs{Path: "nope.txt", Content: "x", Tag: new("0badc0de")}, Opts{})
	wantCode(t, err, CodeNotFound)
	tag := metaStr(&r, "tag")
	if _, err = v.e.Write(ctx, WriteArgs{Path: "deep/er/new.txt", Content: "c\n", Tag: &tag}, Opts{}); err != nil {
		t.Fatal(err)
	}
	crlf, err := New(&Config{Sandbox: v.sb, Roots: []string{v.root}, NewEOL: "crlf", Home: v.home})
	if err != nil {
		t.Fatal(err)
	}
	defer crlf.Close()
	if _, err = crlf.Write(ctx, WriteArgs{Path: "w.txt", Content: "1\n2\n"}, Opts{}); err != nil || v.get(t, "w.txt") != "1\r\n2\r\n" {
		t.Errorf("new_eol crlf: %q %v", v.get(t, "w.txt"), err)
	}
	_, err = v.e.Write(ctx, WriteArgs{Path: ".env", Content: "x"}, Opts{})
	wantCode(t, err, CodeDeniedPath)
	// A byte swapped between write and read-back is write_verify_failed.
	tgt := &target{resolved: filepath.Join(v.root, "w.txt"), rel: "w.txt"}
	ci := commitInfo{sum: [32]byte{1}}
	wantCode(t, v.e.readBack(ctx, tgt, &ci, 0), CodeWriteVerifyFailed)
}

// TestStatTagRacy is CT-34/§5.5: a stat-tag whose mtime is within 2 s of
// the tag needs a content proof; a changed file with an equal stat tuple
// is stale; a quiet file is not re-hashed.
func TestStatTagRacy(t *testing.T) {
	t.Parallel()
	v := newEnv(t)
	body := strings.Repeat("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcde\n", fullHashMax/64+8)
	p := v.put(t, "big.txt", body)
	tag := v.tag(t, "big.txt")
	ent := v.e.cache.tags[tagKey(p, tag)]
	if !ent.racy() || !ent.hasFull {
		t.Fatalf("fresh big file: racy=%v hasFull=%v", ent.racy(), ent.hasFull)
	}
	// Same size and mtime, different bytes: the stat tuple cannot see it,
	// the racy content proof does.
	fi := mustStat(t, p)
	mod := []byte(body)
	mod[10] = 'X'
	if err := os.WriteFile(p, mod, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	edit := []EditItem{{StartLine: new(1), EndLine: new(1), NewText: new("first")}}
	_, err := v.e.Edit(context.Background(), EditArgs{Path: "big.txt", Tag: tag, Edits: edit}, Opts{})
	wantCode(t, err, CodeStaleRead)
	// An old mtime is not racy: the stat tag alone proves it.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	tag = v.tag(t, "big.txt")
	if ent := v.e.cache.tags[tagKey(p, tag)]; ent.racy() || ent.hasFull {
		t.Errorf("quiet file hashed: %+v", ent)
	}
	if _, err := v.e.Edit(context.Background(), EditArgs{Path: "big.txt", Tag: tag, Edits: edit}, Opts{}); err != nil {
		t.Fatal(err)
	}
	if got := v.get(t, "big.txt"); !strings.HasPrefix(got, "first\n0123") || len(got) != len(body)-58 {
		t.Errorf("edit result %q…", got[:20])
	}
}

// TestCodeToolsSymlinkOutsideRoot (ADR-079): a symlink inside the root
// resolving outside is outside_root for reads and writes (headless:
// outside_workspace); deny_paths through a link is denied_path.
func TestCodeToolsSymlinkOutsideRoot(t *testing.T) {
	t.Parallel()
	v := newEnv(t)
	writeFile(t, filepath.Join(v.out, "secret.txt"), "outside\n")
	writeFile(t, filepath.Join(v.root, ".env"), "K=V\n")
	for name, dst := range map[string]string{"out.txt": filepath.Join(v.out, "secret.txt"), "env": filepath.Join(v.root, ".env")} {
		if err := os.Symlink(dst, filepath.Join(v.root, name)); err != nil {
			t.Fatalf("symlink: %v", err)
		}
	}
	ctx := context.Background()
	_, err := v.e.Read(ctx, ReadArgs{Path: "out.txt"}, Opts{})
	wantCode(t, err, CodeOutsideWorkspace)
	_, err = v.e.Write(ctx, WriteArgs{Path: "out.txt", Content: "x"}, Opts{})
	wantCode(t, err, CodeStaleRead) // existing file needs a tag first
	_, err = v.e.Read(ctx, ReadArgs{Path: "env"}, Opts{Approved: true})
	wantCode(t, err, CodeDeniedPath)
	r, err := v.e.Grep(ctx, &GrepArgs{Pattern: "outside|K=V", Path: "."}, Opts{})
	if err != nil || r.Meta["files"] != 0 {
		t.Errorf("walker followed a symlink: %v %v", r.Meta, err)
	}
}

// TestReadOutsideWorkspaceAsks (ADR-069): outside the root a read needs
// approval; approved content is wrapped as untrusted.
func TestReadOutsideWorkspaceAsks(t *testing.T) {
	t.Parallel()
	v := newEnv(t)
	p := filepath.Join(v.out, "o.txt")
	writeFile(t, p, "far\n")
	_, err := v.e.Read(context.Background(), ReadArgs{Path: p}, Opts{})
	wantCode(t, err, CodeOutsideWorkspace)
	r, err := v.e.Read(context.Background(), ReadArgs{Path: p}, Opts{Approved: true})
	if err != nil || !strings.HasPrefix(r.Content, `<untrusted source="file:outside_workspace">`) || !strings.Contains(r.Content, "1|far") {
		t.Errorf("approved read %q %v", r.Content, err)
	}
}

// TestCacheIdleRelease is CT-35: caches are dropped after 10 minutes
// without a file tool call; tags survive.
func TestCacheIdleRelease(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		v := newEnv(t)
		v.put(t, "a.txt", "hello\n")
		tag := v.tag(t, "a.txt")
		v.e.mu.Lock()
		before := v.e.cache.bytes()
		v.e.mu.Unlock()
		if before == 0 {
			t.Fatal("read did not populate caches")
		}
		advance(idleRelease - time.Second)
		synctest.Wait()
		v.e.mu.Lock()
		kept := v.e.cache.bytes()
		v.e.mu.Unlock()
		advance(2 * time.Second)
		synctest.Wait()
		v.e.mu.Lock()
		after, tags := v.e.cache.bytes(), len(v.e.cache.tags)
		v.e.mu.Unlock()
		if kept != before || after != 0 || tags != 1 {
			t.Errorf("bytes %d → %d → %d, tags %d", before, kept, after, tags)
		}
		if _, err := v.e.Edit(context.Background(), EditArgs{Path: "a.txt", Tag: tag, Edits: []EditItem{{OldString: new("hello"), NewString: new("bye")}}}, Opts{}); err != nil {
			t.Fatal(err)
		}
	})
}

// TestCodeToolsMemCap is CT-35/CT-14: cache payload stays within its caps
// (read 16 MB, line index 4 MB, snapshots 16 MB, outline 4 MB) while
// reading many files.
func TestCodeToolsMemCap(t *testing.T) {
	t.Parallel()
	v := newEnv(t)
	chunk := strings.Repeat("y", readCacheFile/2-1) + "\n"
	for i := range 40 {
		v.put(t, fmt.Sprintf("f%02d.txt", i), chunk)
		v.read(t, ReadArgs{Path: fmt.Sprintf("f%02d.txt", i)})
	}
	c := v.e.cache
	if c.read.used > readCacheCap || c.snaps.used > snapshotCap || c.index.used > indexCacheCap || c.outlines.used > outlineCap {
		t.Errorf("caps exceeded: read %d snaps %d index %d", c.read.used, c.snaps.used, c.index.used)
	}
	if total := c.bytes(); total > readCacheCap+snapshotCap+indexCacheCap+outlineCap || total < readCacheCap/2 {
		t.Errorf("cache total %d", total)
	}
}

// FuzzEditApply (E-57): for any file and edit, edit_file either writes
// exactly the engine's result or fails leaving the file untouched — never
// a partial write.
func FuzzEditApply(f *testing.F) {
	f.Add([]byte("a\r\nb\nc"), "b", "B\nX", false, 0, 0, "")
	f.Add([]byte("\xff\xfeh\x00i\x00\n\x00"), "hi", "yo", true, 0, 0, "")
	f.Add([]byte("x\ny\nz\n"), "", "", false, -2, -1, "q")
	f.Add([]byte("\xef\xbb\xbfdup dup\n"), "dup", "", true, 0, 0, "")
	v := newEnv(f)
	f.Fuzz(func(t *testing.T, input []byte, old, repl string, all bool, s, e int, text string) {
		p := v.put(t, "fz.txt", string(input))
		tag := v.tag(t, "fz.txt")
		ed := EditItem{OldString: &old, NewString: &repl, ReplaceAll: all}
		if old == "" {
			ed = EditItem{StartLine: &s, EndLine: &e, NewText: &text}
		}
		want, wantErr := applyPure(input, ed)
		_, err := v.e.Edit(context.Background(), EditArgs{Path: "fz.txt", Tag: tag, Edits: []EditItem{ed}}, Opts{})
		got := readAll(t, p)
		switch {
		case err != nil && !bytes.Equal(got, input):
			t.Fatalf("failed edit (%v) changed the file", err)
		case err == nil && (wantErr != nil || !bytes.Equal(got, want)):
			t.Fatalf("written %q, engine result %q (%v)", got, want, wantErr)
		case err != nil && wantErr == nil:
			t.Fatalf("edit failed (%v) where the engine succeeds", err)
		}
	})
}

// applyPure is the in-memory engine result for one edit.
func applyPure(raw []byte, ed EditItem) ([]byte, error) {
	d, err := parseDoc(raw)
	if err != nil {
		return nil, err
	}
	spans, err := resolveEdits(d, "fz.txt", []EditItem{ed})
	if err != nil {
		return nil, err
	}
	out, ok := d.render(d.apply(spans))
	if !ok {
		return nil, toolErr(CodeInvalidArgs, "", "unrepresentable")
	}
	return out, nil
}

// advance moves the synctest fake clock by d (E-72: no time.Sleep).
func advance(d time.Duration) { <-time.After(d) }
