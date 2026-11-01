package e2e

import (
	"os"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// TestF5ApprovalBounds: a destructive shell call is denied headless and a
// mutating one runs only on the trusted auto path. Both controls share one
// binary and workspace shape.
func TestF5ApprovalBounds(t *testing.T) {
	t.Parallel()
	t.Run("destructive denied without an approver", func(t *testing.T) {
		t.Parallel()
		e := NewTrusted(t,
			ToolCallReply("c1", "shell", `{"command":"rm -rf .git"}`),
			providertest.Text("done"))
		if err := os.MkdirAll(e.Path(".git"), 0o700); err != nil {
			t.Fatal(err)
		}
		e.WriteFile(".git/marker.txt", "keep")
		res := e.Run("remove", "--approval", "auto", "--unsafe-auto-shell")
		res.RequireOK(t)
		if _, err := os.Stat(e.Path(".git/marker.txt")); err != nil {
			t.Fatalf("destructive command executed without an approver: %v", err)
		}
		if !strings.Contains(e.RequestText(t, 1), "denied") {
			t.Fatalf("tool result does not record the denial:\n%s", e.RequestText(t, 1))
		}
	})
	t.Run("mutating runs on the trusted auto path", func(t *testing.T) {
		t.Parallel()
		e := NewTrusted(t,
			ToolCallReply("c1", "shell", `{"command":"printf allowed > allowed.txt"}`),
			providertest.Text("done"))
		res := e.Run("write", "--approval", "auto", "--unsafe-auto-shell")
		res.RequireOK(t)
		if got := e.ReadFile("allowed.txt"); got != "allowed" {
			t.Fatalf("allowed.txt = %q", got)
		}
	})
	t.Run("denial leaks no secret", func(t *testing.T) {
		t.Parallel()
		e := NewTrusted(t,
			ToolCallReply("c1", "shell", `{"command":"git push --force origin HEAD"}`),
			providertest.Text("done"))
		secret := "SECRET-PREVIEW-9f3c"
		res := e.Run("push", append([]string{"--approval", "ask"}, extrasEnv(secret)...)...)
		res.RequireOK(t)
		if strings.Contains(res.Stdout+res.Stderr, secret) {
			t.Fatalf("refusal leaked the secret:\n%s", res.Stdout+res.Stderr)
		}
	})
}

// extrasEnv is the environment of the third control: the value is known to the
// child but must never appear in a refusal or preview.
func extrasEnv(secret string) []string {
	return []string{"DAMNBOT_E2E_SECRET=" + secret}
}

// TestF5BudgetPreTurn: the ledger cap is enforced before the provider call;
// the existing load-bearing tests carry the positive/negative controls.
func TestF5BudgetPreTurn(t *testing.T) {
	t.Run("denies at the ledger cap with zero provider calls", TestE2EBudgetGuardDeniesAtLedgerCap)
	t.Run("allows below the cap", TestE2EBudgetGuardAllowsUnderCap)
}
