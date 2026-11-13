package classify

import "testing"

// bypassRows is the GAP-SEC-12 corpus: 10 bypass flags × 4 invocation forms
// (direct, npx, bunx, pipx run) = 40 commands.
func bypassRows() []struct {
	cmd, agent string
} {
	flags := []struct{ agent, flag string }{
		{"claude", "--dangerously-skip-permissions"},
		{"codex", "--dangerously-bypass-approvals-and-sandbox"},
		{"claude", "--yolo"},
		{"grok", "--always-approve"},
		{"aider", "--auto-approve"},
		{"codex", "--full-auto"},
		{"goose", "--allow-all-tools"},
		{"openhands", "--trust-all-tools"},
		{"gemini", "--approval-mode yolo"},
		{"claude", "--permission-mode bypassPermissions"},
	}
	forms := []func(agent, flag string) string{
		func(agent, flag string) string { return agent + " " + flag + " -p x" },
		func(agent, flag string) string { return "npx -y " + agent + " " + flag + " -p x" },
		func(agent, flag string) string { return "bunx " + agent + " " + flag + " -p x" },
		func(agent, flag string) string { return "pipx run " + agent + " " + flag + " -p x" },
	}
	out := make([]struct{ cmd, agent string }, 0, len(flags)*len(forms))
	for _, f := range flags {
		for _, form := range forms {
			out = append(out, struct{ cmd, agent string }{form(f.agent, f.flag), f.agent})
		}
	}
	return out
}

// TestBypassIgnoresAllowDestructive pins GAP-SEC-12: every delegated
// agent-CLI bypass flag is labelled delegated_agent_bypass, and no approval
// mode, shell_allow match, or auto-approve path can run it without a human.
func TestBypassIgnoresAllowDestructive(t *testing.T) {
	t.Parallel()
	rows := bypassRows()
	if len(rows) != 40 {
		t.Fatalf("corpus = %d, want 40", len(rows))
	}
	for _, row := range rows {
		v := Classify(row.cmd, testEnv())
		if v.Class != DelegatedAgentBypass {
			t.Errorf("Classify(%q) = %s, want delegated_agent_bypass (flags %v)", row.cmd, v.Class, v.Flags.Names())
			continue
		}
		for _, mode := range []Mode{ModeAsk, ModeAutoEdit, ModeAuto, ModeHeadless, ModeUnsafeAutoShell} {
			c := &Context{
				Mode: mode, AllowMatched: true, WorkspaceTrusted: true, CwdInRoot: true,
				Session: false, Stdin: false, TurnTainted: false, WroteWithoutApproval: false,
			}
			if d := Decide(&v, c); d == AutoApprove {
				t.Errorf("Decide(%q, mode %v) = auto; bypass must always ask", row.cmd, mode)
			}
		}
	}
}
