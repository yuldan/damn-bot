package main

import (
	"strings"
	"testing"
)

func TestCompletionScripts(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		first, ok := completionScript(shell)
		if !ok || !strings.Contains(first, "__complete") {
			t.Fatalf("%s script = %q", shell, first)
		}
		second, _ := completionScript(shell)
		if first != second {
			t.Fatalf("%s script is not idempotent", shell)
		}
	}
	if _, ok := completionScript("tcsh"); ok {
		t.Fatal("unsupported shell accepted")
	}
}

func TestCompleteCandidates(t *testing.T) {
	code, out, errOut := runArgs(t, "__complete")
	if code != 0 || errOut != "" || !strings.Contains(out, "sessions") || !strings.Contains(out, "setup") {
		t.Fatalf("__complete = (%d, %q, %q)", code, out, errOut)
	}
	if strings.Contains(out, "__complete") {
		t.Fatal("hidden command leaked into completion")
	}
	code, out, _ = runArgs(t, "__complete", "se")
	if code != 0 || out != "serve\nsessions\nsetup\n" {
		t.Fatalf("prefix se = (%d, %q)", code, out)
	}
	code, out, _ = runArgs(t, "__complete", "zzz")
	if code != 0 || out != "" {
		t.Fatalf("no candidates = (%d, %q), want exit 0/empty", code, out)
	}
	code, out, _ = runArgs(t, "__complete", "config", "")
	if code != 0 || !strings.Contains(out, "show") || !strings.Contains(out, "profiles") {
		t.Fatalf("config subcommands = (%d, %q)", code, out)
	}
	code, out, _ = runArgs(t, "__complete", "config", "profiles", "")
	if code != 0 || out != "export\nimport\nls\n" {
		t.Fatalf("config profiles = (%d, %q)", code, out)
	}
}

func TestSetupCompletion(t *testing.T) {
	code, out, _ := runArgs(t, "setup", "completion", "bash")
	if code != 0 || !strings.Contains(out, "complete -F _damnbot_complete damnbot") {
		t.Fatalf("setup completion bash = (%d, %q)", code, out)
	}
	if code, _, errOut := runArgs(t, "setup", "completion", "tcsh"); code != 2 || !strings.Contains(errOut, "unsupported shell") {
		t.Fatalf("unsupported shell = (%d, %s), want 2", code, errOut)
	}
	if code, _, _ := runArgs(t, "setup", "completion"); code != 2 {
		t.Fatal("missing shell accepted")
	}
}
