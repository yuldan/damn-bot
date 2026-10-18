package codetools

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// glob limits (CODE-TOOLS §3, CT-31).
const (
	globDefaultLimit = 200
	globMaxLimit     = 2000
)

// GlobArgs are the glob arguments (CODE-TOOLS §3).
type GlobArgs struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path,omitempty"`
	Sort    string `json:"sort,omitempty"`
	Offset  int    `json:"offset,omitempty"`
	// Limit: 0 = default; -1 = unlimited (benchmarks only).
	Limit int `json:"limit,omitempty"`
}

// Glob runs glob: doublestar over the ignore-aware walk, byte-ordered paths
// or newest first.
func (e *Engine) Glob(ctx context.Context, a GlobArgs, o Opts) (Result, error) {
	if err := checkGlobArgs(&a); err != nil {
		return Result{}, err
	}
	if a.Limit == 0 {
		a.Limit = globDefaultLimit
	}
	e.mu.Lock()
	e.touch()
	e.mu.Unlock()
	r, err := e.searchRoot(ctx, a.Path, o)
	if err != nil {
		return Result{}, err
	}
	if !r.dir {
		return Result{}, toolErr(CodeInvalidArgs, "glob lists a directory; use read_file for a file", "%s is not a directory", a.Path)
	}
	pat := a.Pattern
	dirs := strings.HasSuffix(pat, "/")
	pat = strings.TrimSuffix(pat, "/")
	spec := &walkSpec{
		root: r.abs, approved: r.outside, dirs: dirs,
		hidden:  pat[0] == '.' || strings.Contains(pat, "/."),
		keepDir: prefixPruner(pat),
	}
	match := func(rel string) bool { return globMatch(pat, rel, filepath.Base(rel)) }
	if dirs {
		spec.keepDirItem = match
		spec.keepFile = func(string) bool { return false }
	} else {
		spec.keepFile = match
	}
	var paths []string
	st, err := e.walk(ctx, spec, r.st, r.top, func(j *fileJob, _ scanOut) bool {
		paths = append(paths, r.join(j.rel))
		return true
	})
	if err != nil {
		return Result{}, &ToolError{Code: CodeInternal, Message: "glob walk", Err: err}
	}
	stats := 0
	if a.Sort == "mtime" {
		stats = e.sortMtime(r, paths)
	}
	return globResult(e, &a, r, paths, st, stats, o), nil
}

func checkGlobArgs(a *GlobArgs) error {
	switch {
	case a.Pattern == "":
		return toolErr(CodeInvalidArgs, `glob requires {"pattern": string}`, "/pattern: required")
	case !doublestar.ValidatePattern(a.Pattern):
		return toolErr(CodeInvalidArgs, "doublestar syntax: **, {a,b}, [...]", "/pattern: invalid %q", a.Pattern)
	case a.Sort != "" && a.Sort != "path" && a.Sort != "mtime":
		return toolErr(CodeInvalidArgs, "", "/sort: path|mtime")
	case a.Limit < -1 || a.Limit > globMaxLimit:
		return toolErr(CodeInvalidArgs, "", "/limit: must be 1..%d", globMaxLimit)
	case a.Offset < 0:
		return toolErr(CodeInvalidArgs, "", "/offset: must be ≥ 0")
	}
	return nil
}

// prefixPruner keeps only directories compatible with the pattern's literal
// leading components ("internal/tools/**" prunes everything else).
func prefixPruner(pat string) func(string) bool {
	if !strings.Contains(pat, "/") {
		return nil
	}
	var lit []string
	for c := range strings.SplitSeq(pat, "/") {
		if strings.ContainsAny(c, `*?[{\`) {
			break
		}
		lit = append(lit, c)
	}
	if len(lit) == 0 {
		return nil
	}
	return func(rel string) bool {
		comps := strings.Split(rel, "/")
		for i := range min(len(comps), len(lit)) {
			if comps[i] != lit[i] {
				return false
			}
		}
		return true
	}
}

// sortMtime orders paths newest first (ties by path); it stats every match
// and reports how many stats it made (Meta.stat_calls).
func (e *Engine) sortMtime(r *searchRoot, paths []string) int {
	mt := make(map[string]int64, len(paths))
	for _, p := range paths {
		rel := strings.TrimSuffix(p, "/")
		if r.rel != "." {
			rel = strings.TrimPrefix(rel, r.rel+"/")
		}
		if fi, err := os.Lstat(filepath.Join(r.abs, filepath.FromSlash(rel))); err == nil {
			mt[p] = fi.ModTime().UnixNano()
		}
	}
	slices.SortStableFunc(paths, func(a, b string) int {
		if c := cmp.Compare(mt[b], mt[a]); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	return len(paths)
}

func globResult(e *Engine, a *GlobArgs, r *searchRoot, paths []string, st *walkStats, stats int, o Opts) Result {
	total := len(paths)
	from := min(a.Offset, total)
	to := total
	if a.Limit >= 0 {
		to = min(total, from+a.Limit)
	}
	var body strings.Builder
	budget := o.pageChars() - 200
	shown := 0
	for _, p := range paths[from:to] {
		if shown > 0 && budget-len(p)-1 < 0 {
			break
		}
		budget -= len(p) + 1
		body.WriteString(p + "\n")
		shown++
	}
	var b strings.Builder
	fmt.Fprintf(&b, "glob %s · %d of %d\n%s", strconv.Quote(a.Pattern), shown, total, body.String())
	meta := map[string]any{"count": total}
	if d := st.denied; d > 0 {
		fmt.Fprintf(&b, "skipped: %d denied\n", d)
	}
	if st.nUnread > 0 {
		fmt.Fprintf(&b, "skipped: %d unreadable %s\n", st.nUnread, strings.Join(st.unreadable, " "))
	}
	if next := from + shown; next < total {
		fmt.Fprintf(&b, "next: offset=%d\n", next)
		meta["next"] = next
	}
	if stats > 0 {
		meta["stat_calls"] = stats
	}
	return Result{Content: e.wrapOutside(&target{outside: r.outside}, strings.TrimSuffix(b.String(), "\n")), Meta: meta}
}
