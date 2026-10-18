package classify

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestOperandLabels covers the operand rules of EXEC-AND-CODING §A.8 on
// the state directly: deny_paths, outside_read vs outside writes, blob
// reads, devices, symlinks, globs and unresolvable operands.
func TestOperandLabels(t *testing.T) {
	t.Parallel()
	fs := fakeFS{
		links: map[string]string{testRoot + "/link": "/etc", testRoot + "/keys": testHome + "/.ssh"},
		globs: map[string][]string{testRoot + "/k*": {testRoot + "/keys"}},
	}
	cases := []struct {
		a     arg
		write bool
		cwdOK bool
		class Class
		flags Flag
	}{
		{arg{s: "src/main.go", lit: true}, false, true, Mutating, 0},
		{arg{s: "src/main.go", lit: true}, true, true, Mutating, 0},
		{arg{s: "/etc/passwd", lit: true}, false, true, Mutating, OutsideRead},
		{arg{s: "/etc/passwd", lit: true}, true, true, Destructive, 0},
		{arg{s: "../x", lit: true}, false, true, Mutating, OutsideRead},
		{arg{s: testHome + "/.ssh/id_rsa", lit: true}, false, true, Mutating, DeniedPath},
		{arg{s: "a/b/.env", lit: true}, false, true, Mutating, DeniedPath},
		{arg{s: "certs/X.PEM", lit: true}, false, true, Mutating, DeniedPath},
		{arg{s: "/proc/12/task/3/environ", lit: true}, false, true, Mutating, DeniedPath},
		{arg{s: "/home/u/.local/share/damnbot/blobs/ab", lit: true}, false, true, Mutating, 0},
		{arg{s: "/home/u/.local/share/damnbot/blobs/ab", lit: true}, true, true, Destructive, 0},
		{arg{s: "/dev/null", lit: true}, true, true, Mutating, 0},
		{arg{s: "-", lit: true}, false, true, Mutating, 0},
		{arg{s: "$X"}, false, true, Unknown, 0},
		{arg{s: "rel", lit: true}, false, false, Unknown, 0},
		{arg{s: "/abs", lit: true}, false, false, Mutating, OutsideRead},
		{arg{s: "link/hosts", lit: true}, false, true, Mutating, OutsideRead},
		{arg{s: "keys/id_rsa", lit: true}, false, true, Mutating, DeniedPath},
		{arg{s: "k*", lit: true, glob: true}, false, true, Mutating, DeniedPath},
		{arg{s: "/tmp/*.log", lit: true, glob: true}, true, true, Destructive, 0},
	}
	for _, c := range cases {
		env := testEnv()
		env.Resolver = fs
		s := newState(env)
		s.cwdOK = c.cwdOK
		s.operand(c.a, c.write)
		if s.v.Class != c.class || s.v.Flags != c.flags {
			t.Errorf("operand(%q, write=%v) = %s %v, want %s %v", c.a.s, c.write, s.v.Class, s.v.Flags.Names(), c.class, c.flags.Names())
		}
	}
}

func TestGlobRootContent(t *testing.T) {
	t.Parallel()
	s := newState(testEnv())
	if got := s.operand(arg{s: "*", lit: true, glob: true}, true); !slices.Contains(got, testRoot) {
		t.Fatalf("rm * at root resolves to %q, want the root itself", got)
	}
	if got := s.operand(arg{s: "*.o", lit: true, glob: true}, true); slices.Contains(got, testRoot) {
		t.Fatalf("*.o resolves to the root: %q", got)
	}
}

func TestWithinAndDeny(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		p, root string
		want    bool
	}{
		{"/a/b", "/a", true},
		{"/a", "/a", true},
		{"/ab", "/a", false},
		{"/x", "/", true},
		{"C:/Proj/x", "c:/proj", true},
		{"C:/proj2", "C:/proj", false},
	} {
		if got := within(c.p, c.root, false); got != c.want {
			t.Errorf("within(%q, %q) = %v", c.p, c.root, got)
		}
	}
	s := newState(&Env{Home: "/h", DenyPaths: []string{"~/.ssh", "**/*.pem", "/proc/*/mem", "[bad"}})
	for p, want := range map[string]bool{
		"/h/.ssh": true, "/h/.SSH/k": true, "/x/y/a.pem": true, "/proc/1/mem": true, "/h/ssh": false, "/proc/1/maps": false,
	} {
		if got := s.denied(p); got != want {
			t.Errorf("denied(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestReasonsBounded(t *testing.T) {
	t.Parallel()
	s := newState(testEnv())
	for i := range 2 * maxReasons {
		s.raise(Unknown, string(rune('a'+i))+strings.Repeat("é", maxReasonLen))
		s.note("dup")
	}
	if len(s.v.Reasons) != maxReasons {
		t.Fatalf("reasons = %d", len(s.v.Reasons))
	}
	for _, r := range s.v.Reasons {
		if len(r) > maxReasonLen || !utf8.ValidString(r) {
			t.Fatalf("reason %q", r)
		}
	}
	if got := (Privileged | Network).Names(); !slices.Equal(got, []string{"privileged", "network"}) {
		t.Fatalf("Names = %q", got)
	}
	for c := Mutating; c <= DelegatedAgentBypass; c++ {
		if c.String() == "" || c.P26() != (c >= Irreversible) {
			t.Fatalf("class %d", c)
		}
	}
}
