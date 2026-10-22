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
	v := Classify("damnbot sessions purge s --yes", env)
	for _, mode := range []Mode{ModeAsk, ModeAutoEdit, ModeAuto, ModeHeadless, ModeUnsafeAutoShell} {
		d := Decide(&v, &Context{Mode: mode, WorkspaceTrusted: true, CwdInRoot: true})
		if d == AutoApprove {
			t.Errorf("mode %v auto-approved destructive maintenance: %v", mode, v.Reasons)
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
