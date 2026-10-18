package codetools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// ignoreVocab is the pattern vocabulary of TestGitignoreConformance: git
// syntax features of CODE-TOOLS §2.3 (anchors, dir-only, **, classes,
// escapes, negation, trailing-space escape).
func ignoreVocab() []string {
	return []string{
		"a", "b", "*.log", "*.txt", "foo/", "/bar", "bar/", "d1/x.log", "**/c", "a/**", "a/**/c", "**/foo/*.txt",
		"d?", "[ab]", "[!a]*.txt", "[[:digit:]]*", "x.*", "*", "!*.txt", "!a", "!foo/", "\\#h", "\\!n", "sp\\ ",
		"#comment", "", "d1/", "/d1/b", "*/c", "**", "foo/**/", "[a-c]/x.log", "!d1/b", "b/c/", "*.lo[g]", "!/bar",
		"**/d2/**", "c/", "[^b]", "x.log ", "a/b", "!**/c",
	}
}

func ignoreNames() []string {
	return []string{"a", "b", "c", "foo", "bar", "d1", "d2", "x.log", "y.txt", "1z.txt", "#h", "!n", "q"} // no trailing-space names: invalid on Windows
}

// TestGitignoreConformance is CT-23: ≥ 500 path decisions over random
// .gitignore / info/exclude / core.excludesFile layouts, compared with
// `git check-ignore --no-index`: 0 differences.
func TestGitignoreConformance(t *testing.T) {
	t.Parallel()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("git is the CT-23 oracle and must be installed: %v", err)
	}
	rng := testRand(23)
	total := 0
	for sc := range 30 {
		total += conformanceScenario(t, git, rng, sc)
	}
	t.Logf("%d cases", total)
	if total < 500 {
		t.Fatalf("only %d cases", total)
	}
}

//nolint:gocognit,gocyclo // builds one random tree and ignore layout per scenario
func conformanceScenario(t *testing.T, git string, rng *rand.Rand, sc int) int {
	v := newEnv(t)
	cmd := exec.CommandContext(t.Context(), git, "init", "-q", v.root) //nolint:gosec // G204: git oracle on a test temp dir
	cmd.Env = gitEnv(v)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	names, vocab := ignoreNames(), ignoreVocab()
	pick := func(n int) string {
		var ls []string
		for range n {
			ls = append(ls, vocab[rng.IntN(len(vocab))])
		}
		return strings.Join(ls, "\n") + "\n"
	}
	// A random tree: directories up to depth 3, a file in most of them.
	var paths []string
	isDir := map[string]bool{}
	for range 12 {
		depth := 1 + rng.IntN(3)
		p := ""
		for d := range depth {
			p = filepath.ToSlash(filepath.Join(p, names[rng.IntN(len(names))]))
			if d < depth-1 || rng.IntN(3) == 0 {
				if _, file := isDir[p]; file && !isDir[p] {
					break
				}
				isDir[p] = true
				if err := os.MkdirAll(filepath.Join(v.root, p), 0o750); err != nil {
					t.Fatal(err)
				}
				continue
			}
			if !isDir[p] {
				isDir[p] = false
				writeFile(t, filepath.Join(v.root, p), "x")
			}
		}
	}
	for p := range isDir {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	writeFile(t, filepath.Join(v.root, ".gitignore"), pick(3+rng.IntN(5)))
	writeFile(t, filepath.Join(v.root, ".git", "info", "exclude"), pick(rng.IntN(3)))
	writeFile(t, filepath.Join(v.home, ".config", "git", "ignore"), pick(rng.IntN(3)))
	for _, p := range paths {
		if isDir[p] && rng.IntN(3) == 0 {
			writeFile(t, filepath.Join(v.root, p, ".gitignore"), pick(1+rng.IntN(3)))
		}
	}
	want := gitIgnored(t, git, v, paths)
	t.Logf("scenario %d: %d paths, %d ignored by git", sc, len(paths), len(want))
	for _, p := range paths {
		if got := engineIgnored(v, p, isDir[p]); got != want[p] {
			t.Errorf("scenario %d: %s (dir=%v): engine %v, git %v\n.gitignore:\n%s", sc, p, isDir[p], got, want[p], v.get(t, ".gitignore"))
		}
	}
	return len(paths)
}

// gitEnv isolates git from the machine's configuration.
func gitEnv(v *env) []string {
	return append(os.Environ(), "HOME="+v.home, "XDG_CONFIG_HOME="+filepath.Join(v.home, ".config"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+filepath.Join(v.home, ".gitconfig"))
}

func gitIgnored(t *testing.T, git string, v *env, paths []string) map[string]bool {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), git, "-C", v.root, "check-ignore", "--no-index", "--stdin", "-z") //nolint:gosec // G204: git oracle
	cmd.Env = gitEnv(v)
	cmd.Stdin = strings.NewReader(strings.Join(paths, "\x00") + "\x00")
	out, err := cmd.Output()
	var ee *exec.ExitError
	if err != nil && (!errors.As(err, &ee) || ee.ExitCode() != 1) {
		t.Fatalf("git check-ignore: %v", err)
	}
	got := map[string]bool{}
	for p := range bytes.SplitSeq(out, []byte{0}) {
		if len(p) > 0 {
			got[string(p)] = true
		}
	}
	return got
}

// engineIgnored decides rel like the walker: an ignored parent hides
// everything below it; each directory adds its own ignore file.
func engineIgnored(v *env, rel string, isDir bool) bool {
	ctx := context.Background()
	st, _ := v.e.ignoreBase(ctx, v.root, v.root)
	st = v.e.pushDirIgnores(ctx, st, v.root, "")
	comps := strings.Split(rel, "/")
	prefix := ""
	for i, c := range comps {
		p := prefix + c
		last := i == len(comps)-1
		if st.ignored(p, !last || isDir) {
			return true
		}
		if last {
			return false
		}
		st = v.e.pushDirIgnores(ctx, st, filepath.Join(v.root, filepath.FromSlash(p)), p+"/")
		prefix = p + "/"
	}
	return false
}

// TestWildmatch pins git wildmatch semantics on the tricky cases.
func TestWildmatch(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		p, s     string
		pathname bool
		want     bool
	}{
		{"a/**/b", "a/b", true, true},
		{"a/**/b", "a/x/y/b", true, true},
		{"**/b", "x/b", true, true},
		{"a/**", "a/x/y", true, true},
		{"a*", "ab/c", true, false},
		{"a*", "ab/c", false, true},
		{"[!a]x", "bx", true, true},
		{"[^a]x", "ax", true, false},
		{"[[:digit:]]9", "19", true, true},
		{"[]a]", "]", true, true},
		{"\\*", "*", true, true},
		{"?", "/", true, false},
		{"[a-c]", "b", true, true},
		{"x[", "x[", true, false},
		{"[[:bogus:]]", "a", true, false},
		{"***/b", "a/b", true, true},
		{"a/*/b", "a/x/b", true, true},
		{"a/*/b", "a/x/y/b", true, false},
	} {
		if got := wildmatch(c.p, c.s, c.pathname); got != c.want {
			t.Errorf("wildmatch(%q, %q, %v) = %v", c.p, c.s, c.pathname, got)
		}
	}
	if s := parseIgnore("", []byte("a\\ \nb \n\\#c\n!\\!d/\n")); fmt.Sprint(s.pats) != "[{a\\  false false true 0} {b false false true 1} {\\#c false false true 0} {\\!d true true true 0}]" {
		t.Errorf("parse %v", s.pats)
	}
}

// TestGitConfigExcludes covers core.excludesFile lookup with include.path
// and the XDG default (CODE-TOOLS §2.3 butir 1); the repo config wins but
// is untrusted (CT27-3).
func TestGitConfigExcludes(t *testing.T) {
	t.Parallel()
	v := newEnv(t)
	ctx := context.Background()
	if got, trusted := v.e.gitConfigExcludes(ctx, ""); got != filepath.Join(v.home, ".config", "git", "ignore") || !trusted {
		t.Errorf("default %q %v", got, trusted)
	}
	writeFile(t, filepath.Join(v.home, ".gitconfig"), "[include]\n\tpath = inc.cfg\n")
	writeFile(t, filepath.Join(v.home, "inc.cfg"), "[core]\n\texcludesFile = \"~/my ignore\"\n")
	if got, trusted := v.e.gitConfigExcludes(ctx, ""); got != filepath.Join(v.home, "my ignore") || !trusted {
		t.Errorf("include.path %q %v", got, trusted)
	}
	abs := filepath.Join(v.root, "x")
	writeFile(t, filepath.Join(v.root, ".git", "config"), "[core]\nexcludesfile = "+filepath.ToSlash(abs)+"\n")
	if got, trusted := v.e.gitConfigExcludes(ctx, v.root); filepath.Clean(got) != abs || trusted {
		t.Errorf("repo config %q %v", got, trusted)
	}
}
