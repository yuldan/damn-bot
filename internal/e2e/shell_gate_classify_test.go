package e2e

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// The classifier reads the whole command line, so the redirect target is what
// makes the call Monetary when path lives in a gated directory — the
// interpreter differs per OS, the decision does not.

// writePaymentSkill lays a payment skill under <config>/skills, the directory
// the binary gates as Monetary (skills.PaymentDirs + Catalog.Dirs, SEC10-13).
// It fails the test when the file cannot be written; a skill that does not
// load makes the child exit before the classifier runs, so the caller asserts
// the loaded state through the workspace-twin control.
func writePaymentSkill(t *testing.T, home string) string {
	t.Helper()
	dir := filepath.Join(spillConfigDir(home), "skills", "stripe-pay")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	const md = "---\nname: stripe-pay\ndescription: Charge a customer through the payment provider\nside_effects: [payment]\n---\nCharge the customer.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(md), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// shellDenialCode reads the child's stream-json output and returns the error
// code of the first shell tool_result whose payload says the call did not run
// (ok=false); "" when the shell call succeeded or no result was emitted.
func shellDenialCode(t *testing.T, out string) string {
	t.Helper()
	for line := range strings.SplitSeq(out, "\n") {
		var ev struct {
			Type string `json:"type"`
			Text string `json:"text"`
			Tool string `json:"tool"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil || ev.Type != "tool_result" || ev.Tool != "shell" {
			continue
		}
		var res struct {
			OK    bool `json:"ok"`
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(ev.Text), &res); err != nil {
			continue
		}
		if res.OK {
			return ""
		}
		return res.Error.Code
	}
	return ""
}

// TestShellGateMonetaryThroughBinary (SEC-15, ADR-133; owner rule: every
// security-boundary surface needs one real-binary e2e with a load-bearing
// negative control): the classifier's monetary decision crosses the real
// binary.
//
// The unit-level counterparts live in internal/exec/classify
// (payment_test.go, decide_test.go, policy_test.go); the audit for
// `sec-review-10-15` found the binary-level pair missing: the shell tool runs
// through the binary in internal/e2e/spill_test.go, but no test asserted a
// classifier decision until now.
func TestShellGateMonetaryThroughBinary(t *testing.T) {
	t.Parallel()
	const marker = "shell-gate-marker"

	// Positive: a harmless command in the workspace runs and its effect is
	// visible, and the tool result reaches the model (two provider calls).
	t.Run("positive", func(t *testing.T) {
		t.Parallel()
		// The workspace must be trusted: --unsafe-auto-shell is dead in an
		// untrusted one (SPEC §14), which is what the refusal leg asserts.
		e := NewTrusted(t)
		// Relative: the shell runs with the workspace as cwd, so the command
		// and the assertion use the same path on every OS (no /var symlink or
		// 8.3 short-name mismatch between the absolute temp path and the root).
		target := "positive.txt"
		e.Server.Push(ToolCallReply("c1", "shell", writeMarkerShellArgs(t, e, target, marker)), providertest.Text("done"))
		res := e.Run("write a marker file", "--approval", "auto", "--unsafe-auto-shell")
		res.RequireOK(t)
		if got := e.ReadFile("positive.txt"); !strings.Contains(got, marker) {
			t.Fatalf("workspace write did not happen: %q", got)
		}
		if got := len(e.Requests()); got != 2 {
			t.Fatalf("provider calls = %d, want 2 (the tool result was not fed back)", got)
		}
	})

	// Refusal: the same command shape, but writing into a gated skill
	// directory, is Monetary — refused even under --unsafe-auto-shell, the
	// strongest auto path (ADR-133: never auto-approved, not by shell_allow,
	// not headless, not by --unsafe-auto-shell). The turn stops with
	// approval_denied, the reason is readable, and no side effect exists.
	t.Run("refusal", func(t *testing.T) {
		t.Parallel()
		e := New(t)
		skill := writePaymentSkill(t, e.home)
		if resolved, err := filepath.EvalSymlinks(skill); err == nil {
			skill = resolved // the classifier compares canonical paths
		}
		target := filepath.Join(skill, "side-effect.txt")
		e.Server.Push(ToolCallReply("c1", "shell", writeMarkerShellArgs(t, e, target, marker)), providertest.Text("done"))
		// stream-json carries both the tool-result event and the stop event, so
		// the refusal's reason is asserted from the child's own output. A policy
		// denial is a tool-level refusal the loop hands back to the model (which
		// then finishes the turn): only an approver refusal stops the turn with
		// approval_denied.
		res := e.Run("charge the customer", "--approval", "auto", "--unsafe-auto-shell", "--output", "stream-json")
		if res.Code != 0 {
			t.Fatalf("exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", res.Code, res.Stdout, res.Stderr)
		}
		// The invariant first: the refused command must not have run.
		if _, err := os.Stat(target); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("refused command still had a side effect: %v", err)
		}
		// The fixture reached the tool when the child's own output (or the
		// provider request that carries the tool result) names the command's
		// target in either spelling: stream-json escapes the path, so on
		// Windows the raw `C:\Users\…` form never appears literally.
		quoted, merr := json.Marshal(skill)
		escaped := string(quoted[1 : len(quoted)-1])
		probe := res.Stdout + e.RequestText(t, 1)
		if merr != nil || (!strings.Contains(probe, skill) && !strings.Contains(probe, escaped) && !strings.Contains(probe, marker)) {
			t.Fatalf("the child did not run the intended command: target=%q marker=%q escaped=%q\nstdout:\n%s",
				skill, marker, escaped, res.Stdout)
		}
		if !strings.Contains(res.Stdout, "shell is not allowed in this mode") {
			t.Fatalf("the model did not receive a readable denial:\n%s", res.Stdout)
		}
		if code := shellDenialCode(t, res.Stdout); code != "denied" {
			t.Fatalf("tool_result error code = %q, want denied\n%s", code, res.Stdout)
		}
	})

	// Load-bearing control: the identical command shape against the workspace
	// runs, with the same payment skill loaded. If the refusal above were the
	// harness being dead (or the skill fixture failing to load) this subtest
	// would fail as well; the only difference between the two is the directory.
	t.Run("control", func(t *testing.T) {
		t.Parallel()
		// The workspace must be trusted: --unsafe-auto-shell is dead in an
		// untrusted one (SPEC §14), which is what the refusal leg asserts.
		e := NewTrusted(t)
		writePaymentSkill(t, e.home)
		target := "side-effect.txt"
		e.Server.Push(ToolCallReply("c1", "shell", writeMarkerShellArgs(t, e, target, marker)), providertest.Text("done"))
		res := e.Run("write the same marker in the workspace", "--approval", "auto", "--unsafe-auto-shell")
		res.RequireOK(t)
		if got := e.ReadFile("side-effect.txt"); !strings.Contains(got, marker) {
			t.Fatalf("workspace twin did not run with the payment skill loaded: %q", got)
		}
	})
}
