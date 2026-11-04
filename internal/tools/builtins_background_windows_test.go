//go:build windows && !minimal

package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/exec"
)

// TestShellBackgroundWindowsCapability pins the F5 Windows surface: the
// schema advertises background, Inspect accepts it (the Job Object makes the
// capability real), and Run returns a registry process id served by the
// process_* tools.
func TestShellBackgroundWindowsCapability(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	sh, closeFn := testShell(t, w)
	defer closeFn()
	if !strings.Contains(string(sh.Schema()), `"background"`) {
		t.Fatalf("schema lost background: %s", sh.Schema())
	}
	args := json.RawMessage(`{"command":"echo hi","background":true}`)
	if _, err := sh.(Inspector).Inspect(context.Background(), Env{Session: testView(t, "S")}, args, PolicyContext{Mode: ModeAsk}); err != nil {
		t.Fatalf("Inspect = %v, want background accepted on Windows", err)
	}
	res, err := sh.Run(context.Background(), Env{Session: testView(t, "S")}, args)
	if err != nil || !res.OK {
		t.Fatalf("Run = %+v err=%v", res, err)
	}
	id, _ := res.Meta["process_id"].(string)
	if id == "" {
		t.Fatalf("meta = %+v", res.Meta)
	}
	bg, ok := sh.(*shellTool).cfg.Background.(*exec.Background)
	if !ok {
		t.Fatal("registry is not exec.Background")
	}
	for range 100 {
		pr, perr := bg.Poll("S", id, 0, 0, "")
		if perr == nil && strings.Contains(string(pr.Output), "hi") {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := bg.Kill(context.Background(), "S", id, "KILL"); err != nil {
		t.Fatalf("kill: %v", err)
	}
}
