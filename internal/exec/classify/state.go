package classify

import (
	"path"
	"slices"
	"strings"
)

// state accumulates the verdict over one Classify call.
type state struct {
	env    *Env
	v      Verdict
	deny   [][]string
	roots  []string
	cwd    string
	cwdOK  bool // false after cd: relative operands are unresolvable
	fold   bool // Windows dialect: case-insensitive, backslash separators
	remote bool // inside ssh '<cmd>': paths are remote, not checked
	depth  int  // nested bash -c / eval / wrappers
	mut    bool // some command is not in the non-mutating set
}

func newState(env *Env) *state {
	s := &state{env: env, fold: env.Dialect >= Pwsh, cwdOK: true}
	for _, r := range env.Roots {
		s.roots = append(s.roots, s.clean(r))
	}
	s.cwd = s.clean(env.Cwd)
	if env.Cwd == "" && len(s.roots) > 0 {
		s.cwd = s.roots[0]
	}
	s.deny = compileDeny(env.DenyPaths, s.clean(env.Home))
	return s
}

// raise lifts the class to at least c and records why.
func (s *state) raise(c Class, why string) {
	s.v.Class = max(s.v.Class, c)
	s.note(why)
}

// flag sets f and records why.
func (s *state) flag(f Flag, why string) {
	s.v.Flags |= f
	s.note(why)
}

// note records a reason (deduplicated, at most maxReasons, each truncated
// to maxReasonLen bytes without splitting a rune).
func (s *state) note(why string) {
	if len(why) > maxReasonLen {
		why = strings.ToValidUTF8(why[:maxReasonLen], "")
	}
	if len(s.v.Reasons) < maxReasons && !slices.Contains(s.v.Reasons, why) {
		s.v.Reasons = append(s.v.Reasons, why)
	}
}

// clean normalises a path: slashes on Windows dialects, then path.Clean.
func (s *state) clean(p string) string {
	if p == "" {
		return ""
	}
	if s.fold {
		p = strings.ReplaceAll(p, `\`, "/")
	}
	return path.Clean(p)
}

// inList reports whether name is a space-delimited member of list (the
// short concatenation stays on the stack).
func inList(list, name string) bool {
	return name != "" && !strings.ContainsAny(name, " \t") && strings.Contains(list, " "+name+" ")
}

func isLetter(c byte) bool { return c|0x20 >= 'a' && c|0x20 <= 'z' }

// arg is one evaluated word. lit = statically known (only ~ and $HOME
// expanded); otherwise s is the source text. glob = unquoted * ? [.
type arg struct {
	s      string
	lit    bool
	glob   bool
	quoted bool // any part was quoted; cmd start treats it as a title
}
