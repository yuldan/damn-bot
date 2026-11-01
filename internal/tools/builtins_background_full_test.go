//go:build unix && !minimal

package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/exec"
)

func TestShellBackgroundStartsProcess(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	sh, closeFn := testShell(t, w)
	defer closeFn()
	out, err := sh.Run(context.Background(), Env{Session: testView(t, "S")}, json.RawMessage(`{"command":"printf bg-ok; sleep 5","background":true}`))
	if err != nil || !out.OK {
		t.Fatalf("background = %+v err=%v", out, err)
	}
	id, ok := out.Meta["process_id"].(string)
	if !ok || id == "" {
		t.Fatalf("meta = %+v", out.Meta)
	}
	//nolint:errcheck // the write result is irrelevant here; the poll below asserts
	if err := sh.(*shellTool).cfg.Background.(*exec.Background).Write("S", id, "", false); err != nil {
		t.Logf("write: %v", err)
	}
	var pr exec.PollResult
	var perr error
	for range 100 {
		//nolint:errcheck // perr is checked below
		pr, perr = sh.(*shellTool).cfg.Background.(*exec.Background).Poll("S", id, 0, 0, "")
		if perr == nil && strings.Contains(string(pr.Output), "bg-ok") {
			break
		}
		<-time.After(5 * time.Millisecond)
	}
	if perr != nil || !strings.Contains(string(pr.Output), "bg-ok") {
		t.Fatalf("poll = %+v err=%v", pr, perr)
	}
	//nolint:errcheck // kill is asserted through the returned info below
	if _, err := sh.(*shellTool).cfg.Background.(*exec.Background).Kill(context.Background(), "S", id, "KILL"); err != nil {
		t.Fatalf("kill: %v", err)
	}
	//nolint:errcheck // checked in the condition
	if _, err := sh.(Inspector).Inspect(context.Background(), Env{Session: testView(t, "S")}, json.RawMessage(`{"command":"echo hi","background":true,"session":"s1"}`), PolicyContext{Mode: ModeAsk}); err == nil {
		t.Fatal("background+session accepted")
	}
}
