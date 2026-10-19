package codetools

import (
	"context"
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
