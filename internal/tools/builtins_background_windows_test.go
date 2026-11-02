//go:build windows && !minimal

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/exec"
)

// TestShellBackgroundWindowsCapability pins the explicit Windows refusal:
// the schema still advertises background, Inspect rejects it as a missing
// capability before approval, and a direct Run maps the registry's
// ErrCapability to module_unavailable instead of exec_failed.
func TestShellBackgroundWindowsCapability(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	sh, closeFn := testShell(t, w)
	defer closeFn()
	if !strings.Contains(string(sh.Schema()), `"background"`) {
		t.Fatalf("schema lost background: %s", sh.Schema())
	}
	args := json.RawMessage(`{"command":"echo hi","background":true}`)
	_, err := sh.(Inspector).Inspect(context.Background(), Env{Session: testView(t, "S")}, args, PolicyContext{Mode: ModeAsk})
	var te *ToolError
	if !errors.As(err, &te) || te.Code != CodeModuleUnavailable || !errors.Is(err, exec.ErrCapability) {
		t.Fatalf("Inspect = %v, want module_unavailable wrapping ErrCapability", err)
	}
	res, err := sh.Run(context.Background(), Env{Session: testView(t, "S")}, args)
	var runErr *ToolError
	if !errors.As(err, &runErr) || runErr.Code != CodeModuleUnavailable || !errors.Is(err, exec.ErrCapability) {
		t.Fatalf("Run = %+v err=%v, want module_unavailable wrapping ErrCapability", res, err)
	}
}
