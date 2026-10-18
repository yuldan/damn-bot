package classify

import (
	"path"
	"slices"
	"strings"
	"unicode"

	"mvdan.cc/sh/v3/syntax"
)

// AllowEntry is one shell_allow entry: exact argv tokens, no prefix match.
type AllowEntry []string

// BinaryID identifies a resolved argv0 binary; "the same binary" means
// equal path and (dev, inode, size, mtime) (EXEC-AND-CODING §A.2).
type BinaryID struct {
	Path    string
	Dev     uint64
	Ino     uint64
	Size    int64
	MtimeNs int64
}

// AllowRequest is one shell_allow match query.
type AllowRequest struct {
	Command string
	Dialect Dialect
	Cwd     string   // call cwd (one-shot calls only; sessions never match)
	Roots   []string // session root plus add_dirs
	Entries []AllowEntry
	// Resolve resolves a bare argv0 through the session PATH now.
	Resolve func(argv0 string) (BinaryID, bool)
	// SessionStart holds argv0 resolutions recorded when the session began.
	SessionStart map[string]BinaryID
}

// forbidden are bytes that make a command ineligible for direct execution
// even when quoted: metacharacters, expansions, globs, braces, escapes,
// comments and history (ADR-079 butir 3, ADR-106 butir 5).
const (
	forbiddenPOSIX = "|&;<>()$\x60\\~*?[]{}!#"
	forbiddenWin   = "|&;<>()$\x60~*?[]{}!#%^@,'\""
)

// DirectArgv returns argv when cmd is exactly one literal simple command
// with no metacharacter, expansion, redirect, assignment or control
// character (U+0000–U+001F except tab, U+007F, other non-printing runes);
// only such commands may run through runner mode (b) execve (ADR-088).
func DirectArgv(cmd string, d Dialect) ([]string, bool) {
	bad := forbiddenPOSIX
	if d >= Pwsh {
		bad = forbiddenWin
	}
	if cmd == "" || strings.ContainsAny(cmd, bad) || strings.ContainsFunc(cmd, func(r rune) bool { return r != '\t' && !unicode.IsPrint(r) }) {
		return nil, false
	}
	if d >= Pwsh {
		argv := strings.Fields(cmd)
		return argv, len(argv) > 0
	}
	call, ok := singleCall(cmd, d == POSIX)
	if !ok {
		return nil, false
	}
	w := walker{s: newState(&Env{}), src: cmd}
	argv := make([]string, 0, len(call.Args))
	for _, wd := range call.Args {
		a := w.word(wd)
		if !a.lit || a.glob {
			return nil, false
		}
		argv = append(argv, a.s)
	}
	return argv, len(argv) > 0
}

// singleCall parses cmd as exactly one plain simple command: no redirect,
// assignment, negation or background.
func singleCall(cmd string, posix bool) (*syntax.CallExpr, bool) {
	f, err := parse(cmd, posix)
	if err != nil || len(f.Stmts) != 1 {
		return nil, false
	}
	st := f.Stmts[0]
	call, ok := st.Cmd.(*syntax.CallExpr)
	if !ok || len(st.Redirs) > 0 || st.Negated || st.Background || st.Coprocess || st.Disown || len(call.Assigns) > 0 {
		return nil, false
	}
	return call, true
}

// MatchAllow reports an exact shell_allow match: one literal simple
// command equal to an entry, cwd inside a root, and a bare argv0 that
// resolves now to the same absolute binary as at session start, outside
// every root (NEW3-04) and, on Windows, a .exe (never .bat/.cmd). Decide
// still applies every other condition.
func MatchAllow(r *AllowRequest) bool {
	argv, ok := DirectArgv(r.Command, r.Dialect)
	if !ok || !slices.ContainsFunc(r.Entries, func(e AllowEntry) bool { return slices.Equal(e, argv) }) {
		return false
	}
	cwd := path.Clean(strings.ReplaceAll(r.Cwd, `\`, "/"))
	a0 := argv[0]
	if !isAbs(cwd) || !withinAny(cwd, r.Roots) || strings.ContainsAny(a0, `/\:`) || a0 == "." || a0 == ".." || r.Resolve == nil {
		return false
	}
	id, ok := r.Resolve(a0)
	start, started := r.SessionStart[a0]
	return ok && started && id == start && allowedBinary(path.Clean(strings.ReplaceAll(id.Path, `\`, "/")), r.Roots, cwd)
}

// allowedBinary: absolute, outside every root and the cwd (NEW3-04: a
// relative PATH entry or a binary planted in the workspace never matches),
// .exe only on Windows, never a script launcher.
func allowedBinary(bin string, roots []string, cwd string) bool {
	if !isAbs(bin) || withinAny(bin, roots) || within(bin, cwd, false) {
		return false
	}
	ext := strings.ToLower(path.Ext(bin))
	if (len(bin) > 1 && bin[1] == ':') || strings.HasPrefix(bin, "//") {
		return ext == ".exe"
	}
	return !inList(" .bat .cmd .ps1 .com ", ext)
}

func withinAny(p string, roots []string) bool {
	return slices.ContainsFunc(roots, func(r string) bool {
		return within(p, path.Clean(strings.ReplaceAll(r, `\`, "/")), false)
	})
}
