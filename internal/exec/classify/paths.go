package classify

import (
	"path"
	"strings"
)

// operand resolves one file operand statically (EXEC-AND-CODING §A.8):
// deny_paths → DeniedPath; outside every root → OutsideRead (read) or
// Destructive (write); not statically resolvable → Unknown. It returns
// the resolved paths for command-specific checks.
func (s *state) operand(a arg, write bool) []string {
	if s.remote || (a.lit && a.s == "-") {
		return nil
	}
	if !a.lit {
		s.raise(Unknown, "operand not static: "+a.s)
		return nil
	}
	p, ok := s.abs(a.s)
	if !ok {
		s.raise(Unknown, "relative operand after cd: "+a.s)
		return nil
	}
	s.mut = s.mut || write
	paths := []string{p}
	if a.glob {
		paths = s.glob(p, write)
	}
	for i, q := range paths {
		paths[i] = s.check(q, write)
	}
	return paths
}

// abs makes p absolute against the tracked cwd; on Windows dialects a
// leading ~ is the home directory (bash words expand it while parsing).
func (s *state) abs(p string) (string, bool) {
	p = s.clean(p)
	if s.fold && (p == "~" || strings.HasPrefix(p, "~/")) {
		p = s.clean(s.env.Home) + p[1:]
	}
	if isAbs(p) {
		return p, true
	}
	if !s.cwdOK || s.cwd == "" {
		return "", false
	}
	return path.Join(s.cwd, p), true
}

// isAbs accepts /x, //unc and drive-letter paths (C:x is drive-relative,
// treated as absolute so it never resolves inside the root).
func isAbs(p string) bool {
	return strings.HasPrefix(p, "/") || (len(p) >= 2 && p[1] == ':' && isLetter(p[0]))
}

// glob expands p through the resolver. The static prefix (the directory
// before the first glob component) is always labeled; it is returned only
// for an all-wildcard last component (rm -r * deletes the directory's
// content).
func (s *state) glob(p string, write bool) []string {
	comps := strings.Split(p, "/")
	n := 0
	for n < len(comps) && !strings.ContainsAny(comps[n], "*?[") {
		n++
	}
	prefix := strings.Join(comps[:n], "/")
	if prefix == "" {
		prefix = "/"
	}
	s.check(prefix, write)
	var out []string
	if n == len(comps)-1 && strings.Trim(comps[n], "*.") == "" {
		out = append(out, prefix)
	}
	if s.env.Resolver != nil {
		if m, err := s.env.Resolver.Glob(p); err == nil {
			for _, q := range m {
				out = append(out, s.clean(q))
			}
		}
	}
	return out
}

// check labels one absolute path and returns its resolved form.
func (s *state) check(p string, write bool) string {
	real := s.resolve(p)
	s.paymentFile(p, real)
	switch {
	case s.denied(p) || (real != p && s.denied(real)):
		s.flag(DeniedPath, "denied path: "+p)
		return real
	case inList(" /dev/null /dev/zero /dev/random /dev/urandom /dev/stdin /dev/stdout /dev/stderr /dev/tty ", p),
		strings.HasPrefix(p, "/dev/fd/"):
		return real
	}
	switch {
	case s.inside(p) && s.inside(real):
	case write:
		s.raise(Destructive, "writes outside workspace: "+p)
	case s.env.BlobDir == "" || !within(real, s.clean(s.env.BlobDir), s.fold):
		s.flag(OutsideRead, "reads outside workspace: "+p)
	}
	return real
}

// resolve returns p with symlinks resolved through the resolver, else p.
func (s *state) resolve(p string) string {
	if r := s.env.Resolver; r != nil {
		if rp, err := r.EvalSymlinks(p); err == nil {
			return s.clean(rp)
		}
	}
	return p
}

// paymentFile raises Monetary when p, lexically or resolved, lies inside a
// payment skill directory (SEC-15): running or reading its scripts is a
// monetary action and is never auto-approved.
func (s *state) paymentFile(p, real string) {
	if s.env.MonetaryKey != nil && len(s.env.MonetaryPaths) > 0 {
		k := s.env.MonetaryKey(p)
		for _, d := range s.env.MonetaryPaths {
			kd := strings.TrimSuffix(s.env.MonetaryKey(d), "/")
			if k == kd || strings.HasPrefix(k, kd+"/") {
				s.raise(Monetary, "payment skill file: "+p)
				return
			}
		}
		return
	}
	for _, d := range s.env.MonetaryPaths {
		if d = s.clean(d); within(p, d, s.fold) || within(real, d, s.fold) {
			s.raise(Monetary, "payment skill file: "+p)
			return
		}
	}
}

func (s *state) inside(p string) bool {
	for _, r := range s.roots {
		if within(p, r, s.fold) {
			return true
		}
	}
	return false
}

func (s *state) isRoot(p string) bool {
	for _, r := range s.roots {
		if p == r || (s.fold && strings.EqualFold(p, r)) {
			return true
		}
	}
	return false
}

// within reports whether p equals root or lies beneath it; drive-letter
// paths and Windows dialects compare case-insensitively.
func within(p, root string, fold bool) bool {
	if len(p) < len(root) {
		return false
	}
	head := p[:len(root)]
	fold = fold || (len(root) >= 2 && root[1] == ':')
	if head != root && (!fold || !strings.EqualFold(head, root)) {
		return false
	}
	return len(p) == len(root) || p[len(root)] == '/' || strings.HasSuffix(root, "/")
}

// compileDeny lower-cases the patterns (deny matching is case-insensitive
// on every OS, like sandbox S15-3); "~" expands to home and relative
// patterns match at any depth.
func compileDeny(patterns []string, home string) [][]string {
	out := make([][]string, 0, len(patterns))
	for _, p := range patterns {
		p = strings.ReplaceAll(p, `\`, "/")
		if p == "~" || strings.HasPrefix(p, "~/") {
			p = home + p[1:]
		}
		comps := splitComps(strings.ToLower(path.Clean(p)))
		if !isAbs(p) && (len(comps) == 0 || comps[0] != "**") {
			comps = append([]string{"**"}, comps...)
		}
		out = append(out, comps)
	}
	return out
}

func splitComps(p string) []string {
	return strings.FieldsFunc(p, func(r rune) bool { return r == '/' })
}

func (s *state) denied(p string) bool {
	comps := splitComps(strings.ToLower(p))
	for _, pat := range s.deny {
		if matchPrefix(pat, comps) {
			return true
		}
	}
	return false
}

// matchPrefix reports whether pat matches comps or one of its ancestors.
func matchPrefix(pat, comps []string) bool {
	if len(pat) == 0 {
		return true
	}
	if pat[0] == "**" {
		for i := range len(comps) + 1 {
			if matchPrefix(pat[1:], comps[i:]) {
				return true
			}
		}
		return false
	}
	if len(comps) == 0 {
		return false
	}
	if !strings.ContainsAny(pat[0], `*?[\`) {
		if pat[0] != comps[0] {
			return false
		}
	} else if ok, err := path.Match(pat[0], comps[0]); err != nil || !ok {
		return false
	}
	return matchPrefix(pat[1:], comps[1:])
}
