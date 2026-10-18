package classify

import "testing"

// TestDecideMatrix: every class/flag against every mode with the default
// context (trusted, clean turn, cwd in root), with and without a
// shell_allow match (SPEC §14.1 shell rows, ADR-079, ADR-088).
func TestDecideMatrix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		v                 Verdict
		matched, notMatch string // decision codes per mode (see decisionCode)
	}{
		{Verdict{Class: Mutating}, "AAAAA", "QQQDA"},
		{Verdict{Class: Mutating, Flags: Network}, "AAAAA", "QQQDA"},
		{Verdict{Class: Unknown}, "PPPDD", "PPPDD"},
		{Verdict{Class: Destructive}, "PPPDD", "PPPDD"},
		{Verdict{Class: Irreversible}, "PPPDD", "PPPDD"},
		{Verdict{Class: Monetary}, "PPPDD", "PPPDD"},
		{Verdict{Class: DelegatedAgentBypass}, "PPPDD", "PPPDD"},
		{Verdict{Class: Mutating, Flags: Privileged}, "CCCDD", "CCCDD"},
		{Verdict{Class: Irreversible, Flags: Privileged}, "PPPDD", "PPPDD"},
		{Verdict{Class: Mutating, Flags: OutsideRead}, "PPPOO", "PPPOO"},
		{Verdict{Class: Mutating, Flags: OutsideRead | Privileged}, "CCCDD", "CCCDD"},
		{Verdict{Class: Unknown, Flags: ReferencesSecret}, "PPPDD", "PPPDD"},
		{Verdict{Class: Mutating, Flags: ReferencesSecret}, "QQQDA", "QQQDA"},
		{Verdict{Class: Destructive, Flags: DeniedPath}, "EEEEE", "EEEEE"},
	}
	for _, c := range cases {
		for _, matched := range []bool{true, false} {
			want := c.notMatch
			if matched {
				want = c.matched
			}
			got := make([]byte, 0, 5)
			for _, m := range modes() {
				ctx := Context{Mode: m, AllowMatched: matched, WorkspaceTrusted: true, CwdInRoot: true}
				got = append(got, decisionCode(Decide(&c.v, &ctx)))
			}
			if string(got) != want {
				t.Errorf("%s %v matched=%v: %s, want %s", c.v.Class, c.v.Flags.Names(), matched, got, want)
			}
		}
	}
}

// TestShellAllowConditions: a shell_allow match is auto only with every
// condition of EXEC-AND-CODING §A.8 (one-shot, no stdin, cwd in root,
// trusted, untainted, no unapproved write since the last manual approval).
func TestShellAllowConditions(t *testing.T) {
	t.Parallel()
	v := Verdict{Class: Mutating}
	for name, mod := range map[string]func(*Context){
		"session":   func(c *Context) { c.Session = true },
		"stdin":     func(c *Context) { c.Stdin = true },
		"cwd":       func(c *Context) { c.CwdInRoot = false },
		"untrusted": func(c *Context) { c.WorkspaceTrusted = false },
		"tainted":   func(c *Context) { c.TurnTainted = true },
		"wrote":     func(c *Context) { c.WroteWithoutApproval = true },
	} {
		c := Context{Mode: ModeAuto, AllowMatched: true, WorkspaceTrusted: true, CwdInRoot: true}
		mod(&c)
		if got := Decide(&v, &c); got != Ask {
			t.Errorf("%s: %s, want ask", name, got)
		}
	}
}

func TestOutputSourceFlags(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		v       Verdict
		trusted bool
		want    string
	}{
		{Verdict{}, true, ""},
		{Verdict{}, false, "workspace"},
		{Verdict{Flags: Network}, true, "shell:network"},
		{Verdict{Flags: Network | OutsideRead}, true, "shell:outside_workspace"},
		{Verdict{Flags: Network}, false, "workspace"},
	} {
		if got := OutputSource(&c.v, c.trusted); got != c.want {
			t.Errorf("OutputSource(%v, %v) = %q, want %q", c.v.Flags.Names(), c.trusted, got, c.want)
		}
	}
}
