package codetools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestGlobLimits is CT-31: default 200, max 2.000, count total, mtime sort
// newest first with path tie-break, directories only with a trailing '/'.
func TestGlobLimits(t *testing.T) {
	t.Parallel()
	v := newEnv(t)
	for i := range 250 {
		v.put(t, fmt.Sprintf("d%d/f%03d.go", i%3, i), "x")
	}
	ctx := context.Background()
	r, err := v.e.Glob(ctx, GlobArgs{Pattern: "*.go"}, Opts{})
	if err != nil || r.Meta["count"] != 250 || r.Meta["next"] != globDefaultLimit || !strings.HasPrefix(r.Content, "glob \"*.go\" · 200 of 250\nd0/f000.go\nd0/f003.go") {
		t.Fatalf("default: %v %q %v", r.Meta, firstLine(r.Content), err)
	}
	for _, a := range []GlobArgs{{Pattern: "*.go", Limit: globMaxLimit + 1}, {Pattern: "["}, {Pattern: "*", Sort: "size"}} {
		_, err = v.e.Glob(ctx, a, Opts{})
		wantCode(t, err, CodeInvalidArgs)
	}
	newest := filepath.Join(v.root, "d2", "f005.go")
	if err = os.Chtimes(newest, time.Now().Add(time.Hour), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	r, err = v.e.Glob(ctx, GlobArgs{Pattern: "d2/**", Sort: "mtime", Limit: 2}, Opts{})
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Split(r.Content, "\n"); lines[1] != "d2/f005.go" || r.Meta["stat_calls"] != 83 {
		t.Errorf("mtime sort %q %v", r.Content, r.Meta)
	}
	r, err = v.e.Glob(ctx, GlobArgs{Pattern: "d*/"}, Opts{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(r.Content, "d0/\nd1/\nd2/") {
		t.Errorf("dirs %q", r.Content)
	}
}

// TestGlobDenied (CT27-6): deny_paths entries are hidden by name from glob;
// only the "skipped: N denied" footer counts them (hidden entries are not
// counted: they are skipped before the deny check).
func TestGlobDenied(t *testing.T) {
	t.Parallel()
	v := newEnv(t)
	for _, f := range []string{"a.go", "k1.pem", "sub/b.go", "sub/k2.pem", "sub/deep/k3.pem", "sub/.env"} {
		v.put(t, f, "x")
	}
	r, err := v.e.Glob(context.Background(), GlobArgs{Pattern: "**"}, Opts{})
	if err != nil || strings.Contains(r.Content, ".pem") || !strings.HasSuffix(r.Content, "\nskipped: 3 denied") || r.Meta["count"] != 2 {
		t.Errorf("glob: %v %v\n%q", err, r.Meta, r.Content)
	}
}
