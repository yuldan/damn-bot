package codetools

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

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
