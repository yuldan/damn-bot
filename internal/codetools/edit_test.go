package codetools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	wantCode(t, v.e.readBack(ctx, tgt, &ci), CodeWriteVerifyFailed)
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
