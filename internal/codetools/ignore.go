package codetools

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/damnbot/damnbot/internal/sandbox"
)

// ignorePat is one parsed gitignore line (gitignore(5), git dir.c).
type ignorePat struct {
	pat     string // wildmatch pattern, leading '/' and trailing '/' removed
	neg     bool   // "!pattern"
	dirOnly bool   // trailing '/'
	nodir   bool   // no '/' in the pattern: match the basename at any depth
	kind    uint8  // fast paths: 0 wildmatch, 1 literal, 2 "*" + literal suffix
}

// ignoreSet is one ignore file; base is its directory relative to the
// matching root ("" or "dir/sub/").
type ignoreSet struct {
	base string
	pats []ignorePat
}

// parseIgnore parses gitignore content exactly like git: '#' comments,
// unescaped trailing spaces trimmed, "!" negation, "\#"/"\!" escapes.
func parseIgnore(base string, data []byte) *ignoreSet {
	s := &ignoreSet{base: base}
	for line := range bytes.SplitSeq(data, []byte{'\n'}) {
		l := trimTrailingSpaces(string(line))
		if l == "" || l[0] == '#' {
			continue
		}
		var p ignorePat
		if l[0] == '!' {
			p.neg, l = true, l[1:]
		}
		if strings.HasSuffix(l, "/") {
			p.dirOnly, l = true, l[:len(l)-1]
		}
		if l == "" {
			continue
		}
		p.nodir = !strings.Contains(l, "/")
		p.pat = strings.TrimPrefix(l, "/")
		switch {
		case !strings.ContainsAny(p.pat, `*?[\`):
			p.kind = 1
		case p.nodir && p.pat[0] == '*' && !strings.ContainsAny(p.pat[1:], `*?[\`):
			p.kind = 2
		}
		s.pats = append(s.pats, p)
	}
	return s
}

func trimTrailingSpaces(l string) string {
	end := len(l)
	for end > 0 && l[end-1] == ' ' {
		if bs := len(l[:end-1]) - len(strings.TrimRight(l[:end-1], `\`)); bs%2 == 1 {
			break // "\ " keeps the space
		}
		end--
	}
	return l[:end]
}

// match returns the decision of the last matching pattern of s for rel
// (relative to the matching root), or decided=false.
func (s *ignoreSet) match(rel string, isDir bool) (ignored, decided bool) {
	if !strings.HasPrefix(rel, s.base) {
		return false, false
	}
	rel = rel[len(s.base):]
	base := rel[strings.LastIndexByte(rel, '/')+1:]
	for i := len(s.pats) - 1; i >= 0; i-- {
		p := &s.pats[i]
		if p.dirOnly && !isDir {
			continue
		}
		var ok bool
		switch {
		case p.kind == 1 && p.nodir:
			ok = base == p.pat
		case p.kind == 1:
			ok = rel == p.pat
		case p.kind == 2:
			ok = strings.HasSuffix(base, p.pat[1:])
		case p.nodir:
			ok = wildmatch(p.pat, base, false)
		default:
			ok = wildmatch(p.pat, rel, true)
		}
		if ok {
			return !p.neg, true
		}
	}
	return false, false
}

// ignoreStack is the ordered list of ignore sets in effect (lowest
// precedence first, CODE-TOOLS §2.3); it is shared, so pushes copy.
type ignoreStack []*ignoreSet

func (st ignoreStack) push(s *ignoreSet) ignoreStack {
	if s == nil || len(s.pats) == 0 {
		return st
	}
	return append(slices.Clip(st), s)
}

// ignored reports the decision for one entry whose parents are not ignored.
func (st ignoreStack) ignored(rel string, isDir bool) bool {
	for _, s := range slices.Backward(st) {
		if ign, ok := s.match(rel, isDir); ok {
			return ign
		}
	}
	return false
}

// ignoreFiles are the per-directory sources, in increasing precedence.
func ignoreFiles() [2]string { return [2]string{".gitignore", ".ignore"} }

// readSmall reads a config/ignore file through the sandbox; missing or
// unreadable files yield nil. A workspace-controlled file (trusted=false)
// is read only when it is a regular file that opens Inside with no
// symlink on its path (git ≥ 2.32 also refuses a symlinked .gitignore);
// any other verdict counts as absent, never as an approval prompt
// (CT27-3), and a FIFO is never opened (CT27-4). Trusted files are the
// fixed global git config, its includes and excludes (ADR-103 butir 4):
// they may lie outside the roots and open like an approved read, where
// deny_paths still wins and the content never reaches the model.
func (e *Engine) readSmall(ctx context.Context, p string, trusted bool) []byte {
	var f *os.File
	var err error
	if trusted {
		var v sandbox.Verdict
		if f, v, err = e.sb.OpenFile(ctx, p, os.O_RDONLY, 0); err == nil && v != sandbox.Inside {
			f, err = e.sb.OpenApproved(ctx, p, os.O_RDONLY, 0)
		}
	} else if fi, lerr := os.Lstat(p); lerr == nil && fi.Mode().IsRegular() {
		f, _, err = e.sb.OpenResolved(ctx, p, false) // nil unless Inside
	}
	if err != nil || f == nil {
		return nil
	}
	b, err := io.ReadAll(io.LimitReader(f, 1<<20))
	cerr := f.Close()
	if err != nil || cerr != nil {
		return nil
	}
	return b
}

// gitConfigExcludes resolves core.excludesFile from the XDG git config and
// ~/.gitconfig (later wins; unset → $XDG_CONFIG_HOME/git/ignore), then
// repo/.git/config, which overrides them but is workspace-controlled: it,
// its includes and its excludes file are read only Inside the roots
// (CODE-TOOLS §2.3, CT27-3). include.path is followed up to depth 5;
// trusted reports whether the global configuration decided.
func (e *Engine) gitConfigExcludes(ctx context.Context, repo string) (excl string, trusted bool) {
	if e.xdg != "" {
		e.scanGitConfig(ctx, filepath.Join(e.xdg, "git", "config"), true, 0, &excl)
	}
	if e.home != "" {
		e.scanGitConfig(ctx, filepath.Join(e.home, ".gitconfig"), true, 0, &excl)
	}
	if excl == "" && e.xdg != "" {
		excl = filepath.Join(e.xdg, "git", "ignore")
	}
	if repo != "" {
		var r string
		if e.scanGitConfig(ctx, filepath.Join(repo, ".git", "config"), false, 0, &r); r != "" {
			return r, false
		}
	}
	return excl, true
}

// scanGitConfig reads core.excludesFile and include.path from one config;
// includes inherit its trust.
func (e *Engine) scanGitConfig(ctx context.Context, file string, trusted bool, depth int, excl *string) {
	data := e.readSmall(ctx, file, trusted)
	if data == nil || depth > 5 {
		return
	}
	section := ""
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l == "" || l[0] == '#' || l[0] == ';' {
			continue
		}
		if l[0] == '[' {
			section = strings.ToLower(strings.TrimSpace(strings.Trim(l, "[]")))
			continue
		}
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			continue
		}
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.Trim(strings.TrimSpace(v), `"`)
		switch {
		case section == "core" && k == "excludesfile":
			*excl = e.expandHome(v, filepath.Dir(file))
		case section == "include" && k == "path":
			e.scanGitConfig(ctx, e.expandHome(v, filepath.Dir(file)), trusted, depth+1, excl)
		}
	}
}

func (e *Engine) expandHome(v, dir string) string {
	switch {
	case (v == "~" || strings.HasPrefix(v, "~/")) && e.home != "":
		return filepath.Join(e.home, v[1:])
	case filepath.IsAbs(v):
		return v
	}
	return filepath.Join(dir, v)
}

// ignoreBase builds the stack in effect at dir (an absolute resolved
// directory inside root): global excludes, then the enclosing repository's
// info/exclude and every .gitignore/.ignore from the repository (or root)
// down to dir's parent. Directories above root are never read (ADR-103).
func (e *Engine) ignoreBase(ctx context.Context, root, dir string) (st ignoreStack, top string) {
	top = root
	for d := dir; ; d = filepath.Dir(d) {
		if isGitDir(filepath.Join(d, ".git")) {
			top = d
			break
		}
		if d == root || filepath.Dir(d) == d || !hasPrefixPath(d, root, e.fold) {
			break
		}
	}
	st = e.repoBase(ctx, top)
	rel, err := filepath.Rel(top, dir)
	if err != nil || rel == "." {
		return st, top
	}
	cur := top
	prefix := ""
	for comp := range strings.SplitSeq(filepath.ToSlash(rel), "/") {
		st = e.pushDirIgnores(ctx, st, cur, prefix)
		cur = filepath.Join(cur, comp)
		prefix = path.Join(prefix, comp) + "/"
	}
	return st, top
}

// pushDirIgnores adds dir's .gitignore and .ignore (prefix = dir relative to
// the matching root with a trailing '/').
func (e *Engine) pushDirIgnores(ctx context.Context, st ignoreStack, dir, prefix string) ignoreStack {
	for _, n := range ignoreFiles() {
		st = st.push(e.loadIgnore(ctx, filepath.Join(dir, n), prefix, false))
	}
	return st
}

// repoBase is the ignore stack at the root of a (nested) repository or,
// without a .git entry at top, of the session root: global excludes, then
// info/exclude.
func (e *Engine) repoBase(ctx context.Context, top string) ignoreStack {
	repo := ""
	if isGitDir(filepath.Join(top, ".git")) {
		repo = top
	}
	var st ignoreStack
	excl, trusted := e.gitConfigExcludes(ctx, repo)
	st = st.push(e.loadIgnore(ctx, excl, "", trusted))
	if repo == "" {
		return st
	}
	return st.push(e.loadIgnore(ctx, filepath.Join(repo, ".git", "info", "exclude"), "", false))
}

// loadIgnore parses an ignore file read by readSmall.
func (e *Engine) loadIgnore(ctx context.Context, file, base string, trusted bool) *ignoreSet {
	if file == "" {
		return nil
	}
	data := e.readSmall(ctx, file, trusted)
	if data == nil {
		return nil
	}
	return parseIgnore(base, data)
}

// isGitDir reports a ".git" entry (directory, or file for worktrees and
// submodules) without following symlinks.
func isGitDir(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && (fi.IsDir() || fi.Mode().IsRegular())
}
