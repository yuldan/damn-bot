package codetools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func grep(t testing.TB, v *env, a *GrepArgs) Result {
	t.Helper()
	r, err := v.e.Grep(context.Background(), a, Opts{})
	if err != nil {
		t.Fatalf("Grep(%+v): %v", a, err)
	}
	return r
}

// TestGrepLimits is CT-30: default 100 / max 500 matches, lines ≤ 500
// chars (window around the match), context 0–5, timeout partial result.
func TestGrepLimits(t *testing.T) {
	t.Parallel()
	v := newEnv(t)
	v.put(t, "many.txt", strings.Repeat("hit\n", 700))
	long := strings.Repeat("a", 2000) + "NEEDLE" + strings.Repeat("b", 2000)
	v.put(t, "long.txt", long+"\n")
	r := grep(t, v, &GrepArgs{Pattern: "hit"})
	if r.Meta["matches"] != grepDefaultLimit || r.Meta["next"] != grepDefaultLimit {
		t.Errorf("default limit: %v", r.Meta)
	}
	r = grep(t, v, &GrepArgs{Pattern: "hit", Limit: grepMaxLimit, Offset: 650})
	if r.Meta["matches"] != 50 || r.Meta["next"] != nil {
		t.Errorf("offset 650: %v", r.Meta)
	}
	for _, a := range []GrepArgs{{Pattern: "hit", Limit: grepMaxLimit + 1}, {Pattern: "hit", Context: 6}, {Pattern: "(", Limit: 1}, {Pattern: "x", Type: "cobol"}, {Pattern: ""}} {
		_, err := v.e.Grep(context.Background(), &a, Opts{})
		wantCode(t, err, CodeInvalidArgs)
	}
	r = grep(t, v, &GrepArgs{Pattern: "NEEDLE"})
	line := strings.Split(r.Content, "\n")[2]
	if want := "1:…" + strings.Repeat("a", 250) + "NEEDLE" + strings.Repeat("b", 244) + "… [col 2001]"; line != want {
		t.Errorf("window %q…", line[:40])
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	r, err := v.e.Grep(ctx, &GrepArgs{Pattern: "hit"}, Opts{})
	if err != nil || !strings.Contains(r.Content, "truncated: timeout") {
		t.Errorf("expired deadline: %q %v", r.Content, err)
	}
}

// TestGrepOutput covers CODE-TOOLS §2.6/§9.1: grouping with tags, context
// merge and "--", modes, smart case, glob/type filters, page cut.
func TestGrepOutput(t *testing.T) {
	t.Parallel()
	v := newEnv(t)
	v.put(t, "a.go", "one\ntwo Sync(\nthree\nfour\nfive\nsix\nseven Sync(\neight\n")
	v.put(t, "b.py", "Sync( here\n")
	tagA, tagB := v.tag(t, "a.go"), v.tag(t, "b.py")
	r := grep(t, v, &GrepArgs{Pattern: `Sync\(`, Context: 1})
	want := fmt.Sprintf("grep \"Sync\\\\(\" · 3 matches · 2 files\na.go #%s\n1-one\n2:two Sync(\n3-three\n--\n6-six\n7:seven Sync(\n8-eight\nb.py #%s\n1:Sync( here", tagA, tagB)
	if r.Content != want {
		t.Errorf("got\n%s\nwant\n%s", r.Content, want)
	}
	if r2 := grep(t, v, &GrepArgs{Pattern: "sync", Mode: "count"}); !strings.Contains(r2.Content, "a.go #"+tagA+" 2\nb.py #"+tagB+" 1") {
		t.Errorf("smart case count %q", r.Content)
	}
	if r2 := grep(t, v, &GrepArgs{Pattern: "sync", Case: "sensitive", Mode: "files"}); r2.Meta["files"] != 0 {
		t.Errorf("sensitive: %v", r.Meta)
	}
	if r2 := grep(t, v, &GrepArgs{Pattern: "Sync", Type: "py", Mode: "files"}); r2.Content != "grep \"Sync\" · 1 items · 1 files\nb.py #"+tagB {
		t.Errorf("type filter %q", r.Content)
	}
	if r2 := grep(t, v, &GrepArgs{Pattern: "Sync", Glob: "*.go", Mode: "files"}); !strings.HasSuffix(r2.Content, "a.go #"+tagA) {
		t.Errorf("glob filter %q", r.Content)
	}
	if r2 := grep(t, v, &GrepArgs{Pattern: "two Sync\\(\\nthree"}); !strings.Contains(r2.Content, "2:two Sync(\n3:three") {
		t.Errorf("multiline %q", r.Content)
	}
	v.put(t, "page.txt", strings.Repeat("p"+strings.Repeat("x", 300)+"\n", 100))
	r, err := v.e.Grep(context.Background(), &GrepArgs{Pattern: "^p", Path: "page.txt"}, Opts{PageChars: ReducedPageChars})
	if err != nil || len(r.Content) > ReducedPageChars || r.Meta["next"] == nil {
		t.Errorf("page cap: %d chars, %v %v", len(r.Content), r.Meta, err)
	}
}

// TestGrepIgnoreRules covers CODE-TOOLS §2.3: .gitignore (also outside a
// repo), .ignore, nested repo reset, hidden files, explicit paths win,
// .git never searched, deny_paths counted without names.
func TestGrepIgnoreRules(t *testing.T) {
	t.Parallel()
	v := newEnv(t)
	for _, f := range []string{"keep.txt", "skip.log", "node_modules/m/x.js", "sub/.ignore", "sub/drop.md", "sub/ok.md", ".hidden/h.txt", ".git/config", "nested/.git/HEAD", "nested/skip.log", "x.pem"} {
		v.put(t, f, "needle\n")
	}
	v.put(t, ".gitignore", "*.log\nnode_modules/\n")
	v.put(t, "sub/.ignore", "drop.md\n")
	r := grep(t, v, &GrepArgs{Pattern: "needle", Mode: "files"})
	var got []string
	for _, l := range strings.Split(r.Content, "\n")[1:] {
		if p, _, ok := strings.Cut(l, " #"); ok {
			got = append(got, p)
		}
	}
	if strings.Join(got, ",") != "keep.txt,nested/skip.log,sub/ok.md" || !strings.Contains(r.Content, "skipped: 1 denied") {
		t.Errorf("files %v\n%s", got, r.Content)
	}
	if r2 := grep(t, v, &GrepArgs{Pattern: "needle", Path: "node_modules/m", Mode: "files"}); r2.Meta["files"] != 1 {
		t.Errorf("explicit ignored dir: %v", r.Meta)
	}
	if r2 := grep(t, v, &GrepArgs{Pattern: "needle", Glob: ".hidden/*", Mode: "files"}); r2.Meta["files"] != 1 {
		t.Errorf("dot glob: %v", r.Meta)
	}
	if r2 := grep(t, v, &GrepArgs{Pattern: "needle", Path: ".hidden", Mode: "files"}); r2.Meta["files"] != 1 {
		t.Errorf("explicit hidden dir: %v", r.Meta)
	}
}

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

// TestCodeToolsDeterministic is CT-22: grep and glob output is
// byte-identical across GOMAXPROCS 1, 2, 4, 8, 16 and 100 runs.
func TestCodeToolsDeterministic(t *testing.T) {
	v := newEnv(t)
	rng := testRand(22)
	for i := range 400 {
		var b strings.Builder
		for range rng.IntN(40) {
			fmt.Fprintf(&b, "w%d %s\n", rng.IntN(50), []string{"alpha", "Beta", "gamma"}[rng.IntN(3)])
		}
		v.put(t, fmt.Sprintf("%c/%c/f%d.txt", 'a'+rng.IntN(4), 'a'+rng.IntN(5), i), b.String())
	}
	run := func() string {
		var s strings.Builder
		for _, a := range []GrepArgs{{Pattern: "beta", Limit: 300, Context: 2}, {Pattern: `w1\d`, Mode: "count", Limit: 50}, {Pattern: "gamma", Mode: "files", Offset: 20}} {
			s.WriteString(grep(t, v, &a).Content)
		}
		r, err := v.e.Glob(context.Background(), GlobArgs{Pattern: "**/f1*.txt"}, Opts{})
		if err != nil {
			t.Fatal(err)
		}
		return s.String() + r.Content
	}
	want := run()
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(0))
	for i := range 100 {
		runtime.GOMAXPROCS([]int{1, 2, 4, 8, 16}[i%5])
		if got := run(); got != want {
			gl, wl := strings.Split(got, "\n"), strings.Split(want, "\n")
			for k := range min(len(gl), len(wl)) {
				if gl[k] != wl[k] {
					t.Fatalf("run %d (GOMAXPROCS %d) differs at line %d: %q vs %q", i, runtime.GOMAXPROCS(0), k, gl[k], wl[k])
				}
			}
			t.Fatalf("run %d (GOMAXPROCS %d) differs in length %d vs %d", i, runtime.GOMAXPROCS(0), len(gl), len(wl))
		}
	}
}
