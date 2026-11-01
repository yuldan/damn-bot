//go:build minimal

package tools

import (
	"context"
	"encoding/json"
	"testing"
)

// TestMinimalShellRejectsBackground: the minimal schema has no background
// property, so the old invalid_args behaviour is preserved (P13 minimal).
func TestMinimalShellRejectsBackground(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	sh, closeFn := testShell(t, w)
	defer closeFn()
	d, _, _ := w.dispatcher(t, ModeAuto, &approverRec{answer: ApprovalAllowOnce}, sh)
	out := d.Dispatch(context.Background(), callOf("shell", `{"command":"echo hi","background":true}`))
	if codeOfOutcome(out) != CodeInvalidArgs {
		t.Fatalf("code = %s", codeOfOutcome(out))
	}
	if _, err := sh.Run(context.Background(), Env{Session: testView(t, "S")}, json.RawMessage(`{"command":"echo hi"}`)); err != nil {
		t.Fatalf("foreground failed: %v", err)
	}
}
