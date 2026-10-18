package sandbox

import (
	"math/rand/v2"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

// oldMatchComp and oldMatchPrefix are the deny matcher before components
// were classified at New (ADR-103 butir 16), kept as the oracle of
// TestDenyDifferential and FuzzDenied: pat is the lower-cased component.
func oldMatchComp(pat, c string) bool {
	if isASCII(c) {
		if !hasMeta(pat) {
			return len(c) == len(pat) && strings.EqualFold(c, pat)
		}
		if suf := pat[1:]; pat[0] == '*' && !hasMeta(suf) {
			return len(c) >= len(suf) && strings.EqualFold(c[len(c)-len(suf):], suf)
		}
	}
	ok, err := path.Match(pat, strings.ToLower(c))
	return ok && err == nil
}

func oldMatchPrefix(pat, comps []string) bool {
	for len(pat) > 0 && pat[0] != "**" {
		if len(comps) == 0 || !oldMatchComp(pat[0], comps[0]) {
			return false
		}
		pat, comps = pat[1:], comps[1:]
	}
	if len(pat) == 0 {
		return true
	}
	for i := range len(comps) + 1 {
		if oldMatchPrefix(pat[1:], comps[i:]) {
			return true
		}
	}
	return false
}

func oldDenied(s *Sandbox, p string) bool {
	comps := splitPath(p)
	for _, pat := range s.deny {
		old := make([]string, len(pat))
		for i, c := range pat {
			old[i] = c.pat
		}
		if oldMatchPrefix(old, comps) {
			return true
		}
	}
	return false
}

// checkDeny compares denied with the old matcher on p and, when p's
// parent is not denied, DeniedEntry with denied.
func checkDeny(t *testing.T, s *Sandbox, p string) {
	t.Helper()
	got := s.denied(p)
	if want := oldDenied(s, p); got != want {
		t.Fatalf("denied(%q) = %v, old matcher %v", p, got, want)
	}
	dir, name := filepath.Split(filepath.Clean(p))
	if dir = filepath.Clean(dir); name == "" || s.denied(dir) {
		return
	}
	if e := s.DeniedEntry(dir, name); e != s.denied(filepath.Join(dir, name)) {
		t.Fatalf("DeniedEntry(%q, %q) = %v, denied = %v", dir, name, e, !e)
	}
}

// denyAlphabet mixes literal, suffix, prefix and glob components, case
// variants and non-ASCII runes whose case folding is irregular (ß, ſ, K).
var denyAlphabet = []string{
	"a", "A", "ab", "aB", ".env", ".ENV", "x.pem", "X.PEM", "pem", "é", "É", "ss", "ß", "ſ", "k", "K", "s",
	"*", "**", "*.pem", "*.PEM", "a*", "AB*", "é*", "*é", "a?", "[ab]", "[!a]*", "*a*", "?", "*ſ", "k*",
}

func randComps(r *rand.Rand, n int) []string {
	cs := make([]string, n)
	for i := range cs {
		cs[i] = denyAlphabet[r.IntN(len(denyAlphabet))]
		if strings.ContainsAny(cs[i], "*?[") && r.IntN(2) == 0 {
			cs[i] = strings.Trim(cs[i], "*?[]!") + "z" // path component, not a pattern
		}
	}
	return cs
}

// TestDenyDifferential: the classified matcher and DeniedEntry agree with
// the old matcher on random patterns and paths.
func TestDenyDifferential(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewPCG(1, 2))
	root := t.TempDir()
	for range 300 {
		var pats []string
		for range 1 + r.IntN(4) {
			p := strings.Join(randComps(r, 1+r.IntN(3)), "/")
			if r.IntN(2) == 0 {
				p = "/" + p
			}
			pats = append(pats, p)
		}
		s, err := New(&Config{Roots: []string{root}, DenyPaths: pats, Home: root})
		if err != nil {
			continue // malformed glob: New refuses it
		}
		for range 50 {
			checkDeny(t, s, "/"+strings.Join(randComps(r, r.IntN(6)), "/"))
		}
	}
}

// FuzzDenied runs checkDeny on one pattern and one path.
func FuzzDenied(f *testing.F) {
	for _, s := range [][2]string{{"*.pem", "/a/B.PEM/c"}, {"/a/**/b*", "/A/x/BC"}, {"ſ*", "/s"}, {"**", "/x"}, {"k?", "/KK"}} {
		f.Add(s[0], s[1])
	}
	root := f.TempDir()
	f.Fuzz(func(t *testing.T, pat, p string) {
		s, err := New(&Config{Roots: []string{root}, DenyPaths: []string{pat}, Home: root})
		if err != nil || !filepath.IsAbs(p) || strings.ContainsRune(p, 0) {
			return
		}
		checkDeny(t, s, p)
	})
}
