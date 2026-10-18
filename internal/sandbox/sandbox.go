// Package sandbox implements the code-tool path sandbox (SPEC §14.2,
// CODE-TOOLS §8, ADR-069, ADR-079): roots + deny_paths verdicts over
// symlink-resolved paths, and race-free opens beneath a root.
//
// It is a boundary only for code tools (read_file, grep, glob, write_file,
// edit_file); shell is not path-sandboxed (ADR-079 butir 8).
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

var (
	// ErrDeniedPath reports a path matching deny_paths (SPEC §4.3 langkah 5
	// error denied_path). It wins over roots and approval, also for reads,
	// and can never be approved.
	ErrDeniedPath = errors.New("sandbox: denied_path")
	// ErrOutsideWorkspace reports that a path verified Inside escaped its
	// root while being opened (symlink race, fd verification failure).
	// Headless callers also map Verdict OutsideRoot to this error.
	ErrOutsideWorkspace = errors.New("sandbox: outside_workspace")
	// ErrNoRoot reports a Config without any root (the root set is never
	// empty, ADR-084 butir 2).
	ErrNoRoot = errors.New("sandbox: no root")
	// ErrInvalidPath reports an empty path or one containing NUL.
	ErrInvalidPath = errors.New("sandbox: invalid path")
	// ErrNotRegular reports an object that is neither a regular file nor a
	// directory (FIFO, socket, device): never opened for a tool (CT27-4).
	ErrNotRegular = errors.New("sandbox: not a regular file")
)

// Op is the access an operation needs.
type Op uint8

// Operations: Write covers create, truncate, append and edit.
const (
	OpRead Op = iota
	OpWrite
)

// Verdict is the non-error outcome of Check.
type Verdict uint8

// Verdicts. Every verdict other than Inside is not an error but needs
// approval: the caller asks the user (SPEC §4.3 langkah 7) or, headless,
// fails with ErrOutsideWorkspace. HardLinked is a regular file inside a
// root with more than one link: another name of it may be a deny_paths
// file the path checks cannot see (S16-7, ADR-097 butir 12).
const (
	Inside Verdict = iota
	OutsideRoot
	HardLinked
)

// String returns the verdict name (inside, outside_root, hardlinked_file).
func (v Verdict) String() string {
	switch v {
	case Inside:
		return "inside"
	case HardLinked:
		return "hardlinked_file"
	case OutsideRoot:
	}
	return "outside_root"
}

// Config describes the sandbox of one session.
type Config struct {
	// Roots are the session cwd plus add_dirs, or only the identity
	// workspace for gateway role user (ADR-084). Must exist.
	Roots []string
	// ReadOnlyExtra are read-only exceptions (the blob dir, scoped by the
	// caller to the identity's sessions). Writes there are OutsideRoot.
	ReadOnlyExtra []string
	// DenyPaths are deny_paths patterns (SPEC §12): "~/" expands to Home,
	// "*"/"?"/"[..]" match within one component, "**" matches any number of
	// components. Relative patterns match at any depth ("x" = "**/x").
	// A match on a directory denies everything beneath it. Placeholders
	// such as <config_dir> must be expanded by the caller.
	DenyPaths []string
	// Home expands "~"; empty means os.UserHomeDir.
	Home string
}

type root struct {
	path     string
	readOnly bool
}

// Sandbox is immutable after New and safe for concurrent use.
type Sandbox struct {
	roots []root
	deny  [][]string
	// denyIDs are the objects that fully literal deny patterns named at New,
	// matched against every opened handle (S16-2).
	denyIDs []os.FileInfo
	// foldRoot compares roots case-insensitively (Windows, EC-WIN-013).
	// deny_paths always match case-insensitively (S15-3).
	foldRoot bool
	// noOpenat2 forces the Linux pre-5.6 fallback (test seam only).
	noOpenat2 bool
}

// New validates cfg, canonicalizes every root (symlinks resolved) and
// compiles DenyPaths. cfg is not retained.
func New(cfg *Config) (*Sandbox, error) {
	if cfg == nil || len(cfg.Roots) == 0 {
		return nil, ErrNoRoot
	}
	s := &Sandbox{foldRoot: runtime.GOOS == "windows"}
	for i, list := range [][]string{cfg.Roots, cfg.ReadOnlyExtra} {
		for _, r := range list {
			p, err := canonicalRoot(r)
			if err != nil {
				return nil, err
			}
			s.roots = append(s.roots, root{path: p, readOnly: i == 1})
		}
	}
	deny, ids, err := compileDeny(cfg.DenyPaths, cfg.Home)
	if err != nil {
		return nil, err
	}
	s.deny, s.denyIDs = deny, ids
	return s, nil
}

func canonicalRoot(r string) (string, error) {
	abs, err := absPath(r)
	if err != nil {
		return "", err
	}
	p, err := resolve(abs)
	if err != nil {
		return "", fmt.Errorf("sandbox: root: %w", err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		return "", fmt.Errorf("sandbox: root: %w", err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("sandbox: root %q: %w", p, fs.ErrInvalid)
	}
	return p, nil
}

// Check classifies path for op: ErrDeniedPath when the lexical or the
// resolved path matches deny_paths; else, when the resolved path lies in a
// root allowing op, ErrNotRegular for an existing FIFO, socket or device
// (Unix; the open re-checks on the handle, CT27-4), HardLinked for an
// existing regular file with more than one link and Inside otherwise; else
// OutsideRoot. resolved is the absolute path
// with every existing component's symlinks resolved; for a write target
// that does not exist yet, its deepest existing ancestor is resolved and
// the missing tail appended lexically.
func (s *Sandbox) Check(ctx context.Context, p string, op Op) (Verdict, string, error) {
	v, resolved, _, err := s.check(ctx, p, op)
	return v, resolved, err
}

func (s *Sandbox) check(ctx context.Context, p string, op Op) (Verdict, string, int, error) {
	if err := ctx.Err(); err != nil {
		return OutsideRoot, "", -1, fmt.Errorf("sandbox: %w", err)
	}
	abs, err := absPath(p)
	if err != nil {
		return OutsideRoot, "", -1, err
	}
	if s.denied(abs) {
		return OutsideRoot, "", -1, ErrDeniedPath
	}
	resolved, err := resolve(abs)
	if err != nil {
		return OutsideRoot, "", -1, err
	}
	if s.denied(resolved) {
		return OutsideRoot, "", -1, ErrDeniedPath
	}
	for i, r := range s.roots {
		if (op == OpRead || !r.readOnly) && hasPathPrefix(resolved, r.path, s.foldRoot) {
			n, err := linkCountPath(resolved)
			if errors.Is(err, ErrNotRegular) {
				return OutsideRoot, "", -1, err
			}
			if err == nil && n > 1 {
				return HardLinked, resolved, i, nil
			}
			return Inside, resolved, i, nil
		}
	}
	return OutsideRoot, resolved, -1, nil
}

// absPath normalizes Windows/MSYS spellings, then filepath.Abs (+Clean).
func absPath(p string) (string, error) {
	if p == "" || strings.ContainsRune(p, 0) {
		return "", ErrInvalidPath
	}
	if runtime.GOOS == "windows" {
		p = normalizeWindows(p)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("sandbox: abs: %w", err)
	}
	return abs, nil
}

// maxLinks bounds dangling-symlink hops (same order as Linux MAXSYMLINKS).
const maxLinks = 40

// resolve resolves symlinks in every existing component of the clean
// absolute path abs. A missing tail is appended lexically (it contains no
// ".." since abs is clean); a dangling symlink is followed to its target so
// a write through it is judged by where it would land. On Windows,
// junctions (ModeIrregular since Go 1.23, not followed by EvalSymlinks)
// are expanded like symlinks (S15-2), so the result is free of reparse
// points and os.Root can open it (S16-1).
func resolve(abs string) (string, error) {
	p, tail := abs, ""
	for links := 0; ; {
		real, err := filepath.EvalSymlinks(p)
		next, isLink := "", false
		switch {
		case err == nil && runtime.GOOS != "windows":
			return filepath.Join(real, tail), nil
		case err == nil:
			if next, isLink, err = firstLink(real); err == nil && !isLink {
				return filepath.Join(real, tail), nil
			}
		case errors.Is(err, fs.ErrNotExist):
			next, isLink, err = danglingTarget(p)
		default:
			err = fmt.Errorf("sandbox: resolve: %w", err)
		}
		if err != nil {
			return "", err
		}
		if isLink {
			if links++; links > maxLinks {
				return "", fmt.Errorf("sandbox: resolve %q: too many links: %w", abs, fs.ErrInvalid)
			}
			p = next
			continue
		}
		parent := filepath.Dir(p)
		if parent == p {
			return "", fmt.Errorf("sandbox: resolve %q: %w", abs, fs.ErrNotExist)
		}
		tail = filepath.Join(filepath.Base(p), tail)
		p = parent
	}
}

// firstLink expands the first component of the existing path p that is a
// symlink or a junction; other reparse points (ModeIrregular without a
// readable link target) are not links.
func firstLink(p string) (string, bool, error) {
	comps := splitPath(p)
	for n := 2; n <= len(comps); n++ {
		pre := joinComps(comps[:n])
		fi, err := os.Lstat(pre)
		if err != nil {
			return "", false, fmt.Errorf("sandbox: resolve: %w", err)
		}
		if fi.Mode()&(fs.ModeSymlink|fs.ModeIrregular) == 0 {
			continue
		}
		target, err := linkTarget(pre)
		if err == nil {
			return filepath.Join(append([]string{target}, comps[n:]...)...), true, nil
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return "", false, err
		}
	}
	return "", false, nil
}

// joinComps is the inverse of splitPath for an absolute path.
func joinComps(comps []string) string {
	return comps[0] + string(filepath.Separator) + filepath.Join(comps[1:]...)
}

// danglingTarget returns the absolute target of p when p is a symlink
// (see linkTarget). A dangling junction is expanded by resolve's firstLink.
func danglingTarget(p string) (string, bool, error) {
	fi, err := os.Lstat(p)
	if err != nil || fi.Mode()&fs.ModeSymlink == 0 {
		return "", false, nil //nolint:nilerr // missing p: caller climbs to the parent.
	}
	target, err := linkTarget(p)
	return target, err == nil, err
}

// linkTarget returns the absolute target of the link p, a relative target
// joined to p's resolved parent (never lexically through a symlinked parent).
func linkTarget(p string) (string, error) {
	target, err := os.Readlink(p)
	if err != nil {
		return "", fmt.Errorf("sandbox: readlink: %w", err)
	}
	if runtime.GOOS == "windows" {
		target = normalizeWindows(target)
	}
	if filepath.IsAbs(target) {
		return filepath.Clean(target), nil
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(p))
	if err != nil {
		return "", fmt.Errorf("sandbox: resolve: %w", err)
	}
	return filepath.Join(parent, target), nil
}

// hasPathPrefix reports whether p equals root or lies beneath it.
func hasPathPrefix(p, root string, fold bool) bool {
	if len(p) < len(root) {
		return false
	}
	head := p[:len(root)]
	if head != root && (!fold || !strings.EqualFold(head, root)) {
		return false
	}
	return len(p) == len(root) || os.IsPathSeparator(p[len(root)]) ||
		os.IsPathSeparator(root[len(root)-1])
}

// compileDeny compiles patterns, lower-cased: deny matching is
// case-insensitive on every OS, since Linux also mounts case-insensitive
// file systems (S15-3). An absolute pattern whose glob-free prefix resolves
// elsewhere (symlinked deny dir or Home: ~/.ssh -> dotfiles/ssh, /home ->
// /var/home) also yields its canonical form, so both spellings match (S15-1).
func compileDeny(patterns []string, home string) ([][]string, []os.FileInfo, error) {
	out := make([][]string, 0, len(patterns))
	var ids []os.FileInfo
	for _, pat := range patterns {
		if pat == "~" || strings.HasPrefix(pat, "~/") || strings.HasPrefix(pat, `~\`) {
			if home == "" {
				h, err := os.UserHomeDir()
				if err != nil {
					return nil, nil, fmt.Errorf("sandbox: deny_paths %q: %w", pat, err)
				}
				home = h
			}
			pat = filepath.Join(home, pat[1:])
		}
		comps, err := compilePattern(pat)
		if err != nil {
			return nil, nil, err
		}
		if canon, ok := canonicalPattern(comps); ok { // before lowerAll: resolves on disk
			ids = appendID(ids, canon)
			out = append(out, lowerAll(canon))
		}
		ids = appendID(ids, comps)
		out = append(out, lowerAll(comps))
	}
	return out, ids, nil
}

// appendID appends the identity of the object a fully literal absolute
// pattern names, when it exists (S16-2). os.SameFile(fi, fi) makes Windows
// read the file ID now, at New, rather than lazily at comparison time.
func appendID(ids []os.FileInfo, comps []string) []os.FileInfo {
	if len(comps) == 0 || comps[0] == "**" || slices.ContainsFunc(comps[1:], isGlob) {
		return ids
	}
	fi, err := os.Stat(joinComps(comps))
	if err != nil || !os.SameFile(fi, fi) {
		return ids
	}
	return append(ids, fi)
}

func isGlob(c string) bool { return c == "**" || hasMeta(c) }

// hasMeta reports the bytes that make a pattern component a glob; a byte
// loop, since strings.ContainsAny costs a set lookup per byte on the
// per-entry deny path (ADR-103).
func hasMeta(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return r == '*' || r == '?' || r == '[' || r == '\\' }) >= 0
}

// canonicalPattern resolves the glob-free prefix of an absolute pattern at
// New. A prefix that cannot be resolved (no permission, loop) keeps only
// the configured form: links created later are caught after open (S16-2).
func canonicalPattern(comps []string) ([]string, bool) {
	if len(comps) == 0 || comps[0] == "**" {
		return nil, false // relative: matches at any depth already
	}
	n := 1
	for n < len(comps) && !isGlob(comps[n]) {
		n++
	}
	real, err := resolve(joinComps(comps[:n]))
	if err != nil {
		return nil, false
	}
	canon := append(splitPath(real), comps[n:]...)
	if slices.EqualFunc(canon, comps, strings.EqualFold) {
		return nil, false
	}
	return canon, true
}

func lowerAll(comps []string) []string {
	for i, c := range comps {
		comps[i] = strings.ToLower(c)
	}
	return comps
}

// compilePattern splits pat into components; a relative pattern (on
// Windows also a volume-less "/x") is anchored at any depth with "**".
func compilePattern(pat string) ([]string, error) {
	if runtime.GOOS == "windows" {
		pat = normalizeWindows(pat)
	}
	comps := splitPath(pat)
	if !filepath.IsAbs(pat) {
		if len(comps) > 0 && comps[0] == "" {
			comps = comps[1:]
		}
		if len(comps) == 0 || comps[0] != "**" {
			comps = append([]string{"**"}, comps...)
		}
	}
	for _, c := range comps {
		if _, err := path.Match(c, ""); err != nil {
			return nil, fmt.Errorf("sandbox: deny_paths %q: %w", pat, err)
		}
	}
	return comps, nil
}

// splitPath splits p into components; an absolute path starts with its
// volume name ("" on Unix, "C:" or `\\srv\share` on Windows).
func splitPath(p string) []string { return appendComps(nil, p) }

// matchComp matches path component c case-insensitively against the
// lower-cased pattern component pat. Literal and "*suffix" patterns
// (".env", "*.pem": nearly every real one) compare ASCII components in
// place, without path.Match or allocation, since walkers check every
// directory entry (ADR-103); anything else is lower-cased first, exactly
// like the patterns were (S15-3).
func matchComp(pat, c string) bool {
	if isASCII(c) {
		if !hasMeta(pat) {
			return len(c) == len(pat) && strings.EqualFold(c, pat)
		}
		if suf := pat[1:]; pat[0] == '*' && !hasMeta(suf) {
			return len(c) >= len(suf) && strings.EqualFold(c[len(c)-len(suf):], suf)
		}
	}
	ok, err := path.Match(pat, strings.ToLower(c)) // err: validated in New
	return ok && err == nil
}

func isASCII(s string) bool {
	for i := range len(s) {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// denied matches p against the deny patterns; the components live in a
// stack buffer (no allocation for ASCII paths up to 64 components).
func (s *Sandbox) denied(p string) bool {
	var buf [64]string
	comps := appendComps(buf[:0], p)
	for _, pat := range s.deny {
		if matchPrefix(pat, comps) {
			return true
		}
	}
	return false
}

// appendComps appends the components of p (as splitPath) to dst.
func appendComps(dst []string, p string) []string {
	vol := filepath.VolumeName(p)
	rest := p[len(vol):]
	if vol != "" || (rest != "" && os.IsPathSeparator(rest[0])) {
		dst = append(dst, vol)
	}
	for c := range strings.FieldsFuncSeq(rest, isSep) {
		dst = append(dst, c)
	}
	return dst
}

func isSep(r rune) bool { return r == '/' || r == filepath.Separator }

// matchPrefix reports whether pat matches comps or one of its ancestors.
func matchPrefix(pat, comps []string) bool {
	for len(pat) > 0 && pat[0] != "**" {
		if len(comps) == 0 || !matchComp(pat[0], comps[0]) {
			return false
		}
		pat, comps = pat[1:], comps[1:]
	}
	if len(pat) == 0 {
		return true
	}
	for i := range len(comps) + 1 {
		if matchPrefix(pat[1:], comps[i:]) {
			return true
		}
	}
	return false
}
