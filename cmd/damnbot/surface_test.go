package main

import (
	"bytes"
	"strings"
	"testing"
)

// runArgs is the in-process entry point every CLI test uses.
func runArgs(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = run(args, &out, &errb)
	return code, out.String(), errb.String()
}

// TestParseGlobalArgs: --profile is taken before dispatch, in the prefix
// position and immediately after the command name; anything later belongs to
// the command (a prompt is never eaten).
func TestParseGlobalArgs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		args        []string
		wantProfile string
		wantRest    []string
		wantErr     bool
	}{
		{name: "none", args: nil, wantRest: []string{}},
		{name: "prefix", args: []string{"--profile", "work", "sessions", "ls"}, wantProfile: "work", wantRest: []string{"sessions", "ls"}},
		{name: "prefix_equals", args: []string{"--profile=work", "run", "hi"}, wantProfile: "work", wantRest: []string{"run", "hi"}},
		{name: "after_command", args: []string{"sessions", "--profile", "work", "ls"}, wantProfile: "work", wantRest: []string{"sessions", "ls"}},
		{name: "after_command_equals", args: []string{"run", "--profile=w2", "-p", "hi"}, wantProfile: "w2", wantRest: []string{"run", "-p", "hi"}},
		{name: "prompt_keeps_flag", args: []string{"run", "hi", "--profile"}, wantRest: []string{"run", "hi", "--profile"}},
		{name: "missing_value", args: []string{"--profile"}, wantErr: true},
		{name: "empty_value", args: []string{"--profile="}, wantErr: true},
		{name: "twice", args: []string{"--profile", "a", "--profile", "b"}, wantErr: true},
		{name: "invalid_name", args: []string{"--profile", "Bad Name", "run"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g, rest, err := parseGlobalArgs(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseGlobalArgs(%q) = nil error", tc.args)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseGlobalArgs(%q): %v", tc.args, err)
			}
			if g.profile != tc.wantProfile {
				t.Errorf("profile = %q, want %q", g.profile, tc.wantProfile)
			}
			if strings.Join(rest, "\x00") != strings.Join(tc.wantRest, "\x00") {
				t.Errorf("rest = %q, want %q", rest, tc.wantRest)
			}
		})
	}
}

// TestSurfaceRegistry: one entry per SPEC §10 row, unique names, phases from
// the owning phase, and every callable entry has a phase ≤ F3 today.
func TestSurfaceRegistry(t *testing.T) {
	t.Parallel()
	entries := surface()
	if len(entries) != 22 {
		t.Fatalf("registry has %d entries, want 22 (ADR-197)", len(entries))
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if seen[e.Name] {
			t.Errorf("duplicate command %q", e.Name)
		}
		seen[e.Name] = true
		if e.Phase < phaseF1 || e.Phase > phaseF8 {
			t.Errorf("%s: invalid phase F%d", e.Name, e.Phase)
		}
		if e.Summary == "" || len(e.Summary) > 72 {
			t.Errorf("%s: summary %q is empty or longer than 72 characters", e.Name, e.Summary)
		}
		for _, r := range e.Summary {
			if r < 0x20 || r > 0x7e {
				t.Fatalf("%s: summary is not printable ASCII: %q", e.Name, e.Summary)
			}
		}
	}
	for _, want := range []string{"run", "sessions", "checkpoints", "undo", "review", "skills", "bundles", "version", "acp", "serve", "mcp", "kanban"} {
		if !seen[want] {
			t.Errorf("registry is missing SPEC §10 command %q", want)
		}
	}
}

// TestSurfaceStatus: status derivation matches ADR-080/ADR-197.
func TestSurfaceStatus(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"sessions": surfaceAvailable,
		"bundles":  surfaceAvailable,
		"version":  surfaceBuiltin,
		"acp":      "pending:F3",
		"serve":    "pending:F4",
		"mcp":      "pending:F5",
		"kanban":   "pending:F7",
		"eval":     "pending:F8",
		"doctor":   "pending:F3",
		"config":   "pending:F3",
	}
	for name, want := range cases {
		e, ok := lookupSurface(name)
		if !ok {
			t.Fatalf("%s: missing from registry", name)
		}
		if got := e.Status(); got != want {
			t.Errorf("%s: status = %q, want %q", name, got, want)
		}
		if (e.Run == nil) != strings.HasPrefix(e.Status(), "pending:") {
			t.Errorf("%s: Run and pending status disagree", name)
		}
	}
}

// TestPendingCommandsAreNotCallable: a documented phase > F3 command must not
// be a stub; it exits 2 through the root usage.
func TestPendingCommandsAreNotCallable(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"acp", "serve", "service", "setup", "import", "config", "mcp", "gateway", "cron", "kanban", "usage", "eval", "doctor", "update"} {
		if code, _, _ := runArgs(t, name); code != 2 {
			t.Errorf("%s: exit = %d, want 2 (not callable yet)", name, code)
		}
	}
}

// TestHelpRoot: `damnbot help`, `--help` and `-h` list only callable commands.
func TestHelpRoot(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"help"}, {"--help"}, {"-h"}} {
		code, out, errOut := runArgs(t, args...)
		if code != 0 {
			t.Fatalf("%q: exit = %d, stderr=%q", args, code, errOut)
		}
		if !strings.Contains(out, "Commands:") || !strings.Contains(out, "sessions") || !strings.Contains(out, "version") {
			t.Errorf("%q: help output incomplete:\n%s", args, out)
		}
		if strings.Contains(out, "serve") || strings.Contains(out, "doctor") {
			t.Errorf("%q: help lists a command that is not callable:\n%s", args, out)
		}
		if !strings.Contains(out, "--profile <name>") {
			t.Errorf("%q: help does not document the global --profile:\n%s", args, out)
		}
	}
}

// TestHelpTopic: known topics print summary + usage + phase; pending topics
// say so; unknown topics exit 2.
func TestHelpTopic(t *testing.T) {
	t.Parallel()
	code, out, _ := runArgs(t, "help", "sessions")
	if code != 0 {
		t.Fatalf("help sessions: exit = %d", code)
	}
	for _, want := range []string{"damnbot sessions", "phase: F2 (available)", "usage: damnbot sessions"} {
		if !strings.Contains(out, want) {
			t.Errorf("help sessions missing %q:\n%s", want, out)
		}
	}
	code, out, _ = runArgs(t, "help", "serve")
	if code != 0 || !strings.Contains(out, "pending:F4") {
		t.Fatalf("help serve = (%d, %q), want pending F4 notice", code, out)
	}
	if code, _, _ := runArgs(t, "help", "nope"); code != 2 {
		t.Errorf("help nope: exit = %d, want 2", code)
	}
	if code, _, _ := runArgs(t, "help", "a", "b"); code != 2 {
		t.Errorf("help with two topics: exit = %d, want 2", code)
	}
}

// TestCommandHelpFlag: `<cmd> --help` is served from the registry and keeps
// the SPEC §10 text for run --unsafe-auto-shell.
func TestCommandHelpFlag(t *testing.T) {
	t.Parallel()
	code, out, _ := runArgs(t, "run", "--help")
	if code != 0 || !strings.Contains(out, "damnbot run") || !strings.Contains(out, "-unsafe-auto-shell") {
		t.Fatalf("run --help = (%d, %q)", code, out)
	}
	if !strings.Contains(out, "runs arbitrary shell commands chosen by the model without confirmation; use only in a disposable container") {
		t.Errorf("run --help lost the mandatory SPEC §10 warning:\n%s", out)
	}
	code, out, _ = runArgs(t, "sessions", "-h")
	if code != 0 || !strings.Contains(out, "usage: damnbot sessions") {
		t.Fatalf("sessions -h = (%d, %q)", code, out)
	}
	code, out, _ = runArgs(t, "undo", "--help")
	if code != 0 || !strings.Contains(out, "usage: damnbot undo") {
		t.Fatalf("undo --help = (%d, %q)", code, out)
	}
}

// TestVersionCommand: SPEC §10 `version [--verbose]` is a documented command.
func TestVersionCommand(t *testing.T) {
	t.Parallel()
	code, out, _ := runArgs(t, "version")
	if code != 0 || out != "damnbot dev\n" {
		t.Fatalf("version = (%d, %q)", code, out)
	}
	code, out, _ = runArgs(t, "version", "--verbose")
	if code != 0 || !strings.Contains(out, "sqlite 3.") || !strings.Contains(out, "supported=true") {
		t.Fatalf("version --verbose = (%d, %q)", code, out)
	}
	if code, _, _ := runArgs(t, "version", "extra"); code != 2 {
		t.Errorf("version extra: exit = %d, want 2", code)
	}
}

// TestGlobalFlagRejectsWhenNoCommand: a global flag alone is a usage error,
// not a silent success.
func TestGlobalFlagRejectsWhenNoCommand(t *testing.T) {
	t.Parallel()
	code, out, errOut := runArgs(t, "--profile", "work")
	if code != 2 || out != "" || errOut == "" {
		t.Fatalf("--profile alone = (%d, %q, %q), want usage error", code, out, errOut)
	}
	if code, _, _ := runArgs(t, "--profile", "Bad Name", "version"); code != 2 {
		t.Errorf("invalid profile: exit = %d, want 2", code)
	}
}
