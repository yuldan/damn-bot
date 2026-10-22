package classify

import "testing"

// TestDamnbotMaintenanceDestructive (SEC15-2): our own destructive
// maintenance verbs are Destructive in every dialect, including wrapped,
// relative and absolute invocations; unsafe-auto-shell can never run them.
func TestDamnbotMaintenanceDestructive(t *testing.T) {
	t.Parallel()
	env := testEnv()
	cases := []struct {
		cmd string
		d   Dialect
	}{
		{"damnbot sessions purge s --yes", Bash},
		{"damnbot sessions prune", Bash},
		{"damnbot sessions archive s", Bash},
		{"damnbot sessions restore f", Bash},
		{"damnbot sessions backup restore f", Bash},
		{"damnbot checkpoints clear --yes", Bash},
		{"damnbot checkpoints clear-legacy", Bash},
		{"damnbot checkpoints prune", Bash},
		{"damnbot backup restore f", Bash},
		{"./damnbot sessions purge s", Bash},
		{"/usr/local/bin/damnbot sessions purge s", Bash},
		{"damnbot --profile prod sessions purge s", Bash},
		{"env FOO=1 damnbot checkpoints clear", Bash},
		{"command damnbot sessions purge --yes", Bash},
		{"sudo damnbot sessions purge", Bash},
		{"sh -c 'damnbot sessions purge --yes'", Bash},
		{"damnbot doctor gc", Bash},
		{"damnbot setup --uninstall --purge-data", Bash},
		{"damnbot update --rollback --data", Bash},
		{"damnbot sessions purge s --yes", Cmd},
		{"damnbot sessions purge s --yes", Pwsh},
		{`C:\tools\damnbot.exe sessions purge s --yes`, Cmd},
		{`& 'C:\tools\damnbot.exe' checkpoints clear`, Pwsh},
		{`DAMNBO~1.EXE sessions purge s --yes`, Cmd},
		{`DAMNBO~1.EXE sessions purge s --yes`, Pwsh},
		{`& 'C:\tools\DAMNBO~1.EXE' sessions purge s --yes`, Pwsh},
		{`damnbot. sessions purge s --yes`, Cmd},
		{`damnbot.exe. sessions purge s --yes`, Pwsh},
		{`cmd /c DAMNBO~1.EXE sessions purge s --yes`, Cmd},
		{"damnbot --config /x sessions purge s --yes", Bash},
		{"damnbot -p prod sessions purge s --yes", Bash},
		{"damnbot --data-dir /x sessions purge s --yes", Bash},
		{"exec -a x damnbot sessions purge s --yes", Bash},
		{"env -a x damnbot sessions purge s --yes", Bash},
		{"env --argv0 x damnbot sessions purge s --yes", Bash},
		{"command exec -a x damnbot sessions purge s --yes", Bash},
		{"nohup env -a x damnbot sessions purge s --yes", Bash},
		{"timeout 5 env --argv0 x damnbot sessions purge s --yes", Bash},
		{"bash -c 'exec -a x damnbot sessions purge s --yes'", Bash},
	}
	for _, c := range cases {
		env.Dialect = c.d
		if v := Classify(c.cmd, env); v.Class != Destructive {
			t.Errorf("%v %q: class %s, want Destructive (%v)", c.d, c.cmd, v.Class, v.Reasons)
		}
	}
}

// TestDamnbotMaintenanceNeverAuto (SEC15-2): Destructive is never
// auto-approved, including --unsafe-auto-shell; headless denies it.
func TestDamnbotMaintenanceNeverAuto(t *testing.T) {
	t.Parallel()
	env := testEnv()
	for _, cmd := range []string{
		"damnbot sessions purge s --yes",
		"exec -a x damnbot sessions purge s --yes",
		"env -a x damnbot sessions purge s --yes",
		"env --argv0 x damnbot sessions purge s --yes",
		"command exec -a x damnbot sessions purge s --yes",
		"nohup env -a x damnbot sessions purge s --yes",
		"timeout 5 env --argv0 x damnbot sessions purge s --yes",
	} {
		v := Classify(cmd, env)
		for _, mode := range []Mode{ModeAsk, ModeAutoEdit, ModeAuto, ModeHeadless, ModeUnsafeAutoShell} {
			d := Decide(&v, &Context{Mode: mode, WorkspaceTrusted: true, CwdInRoot: true})
			if d == AutoApprove {
				t.Errorf("%q mode %v auto-approved destructive maintenance: %v", cmd, mode, v.Reasons)
			}
		}
	}
}

// TestDamnbotMaintenanceNegative (SEC15-2): read-only session/checkpoint
// commands and unrelated damnbot commands stay non-destructive.
func TestDamnbotMaintenanceNegative(t *testing.T) {
	t.Parallel()
	env := testEnv()
	for _, cmd := range []string{
		"damnbot sessions ls",
		"damnbot sessions show s",
		"damnbot checkpoints list",
		"damnbot sessions backup verify f",
		"damnbot version",
		"damnbot --help",
		"damnbot run --yes",
	} {
		if v := Classify(cmd, env); v.Class == Destructive {
			t.Errorf("%q: class Destructive, want non-destructive (%v)", cmd, v.Reasons)
		}
	}
}

// TestDamnbotSymlinkAndCopies (SEC15-6): a path resolving to the binary's
// name is caught; an arbitrary alias/copy name is a documented static
// limitation.
func TestDamnbotSymlinkAndCopies(t *testing.T) {
	t.Parallel()
	env := testEnv()
	env.Resolver = fakeFS{links: map[string]string{testRoot + "/db": "/usr/local/bin/damnbot"}}
	if v := Classify("./db sessions purge s --yes", env); v.Class != Destructive {
		t.Errorf("symlinked binary: class %s, want Destructive (%v)", v.Class, v.Reasons)
	}
	if v := Classify("./x sessions purge s --yes", env); v.Class == Destructive {
		t.Errorf("copy with arbitrary name classified Destructive; document as static-unobservable (%v)", v.Reasons)
	}
}
