//go:build unix

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/exec"
)

func procTools(t *testing.T, reg *exec.Background) map[string]Tool {
	t.Helper()
	out := map[string]Tool{}
	for _, tl := range NewProcessTools(&ProcessConfig{Registry: reg}) {
		out[tl.Name()] = tl
	}
	return out
}

func startSleeper(t *testing.T, reg *exec.Background, session, script string) exec.ProcessInfo {
	t.Helper()
	info, err := reg.Start(t.Context(), session, []string{"/bin/sh", "-c", script}, "/tmp", []string{"PATH=/usr/bin:/bin"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return info
}

func TestProcessToolsSessionIsolation(t *testing.T) {
	t.Parallel()
	reg := exec.NewBackground()
	t.Cleanup(func() {
		//nolint:errcheck,gosec // test cleanup
		reg.Close(context.Background())
	})
	tools := procTools(t, reg)
	info := startSleeper(t, reg, "S", "sleep 30")
	envOwner := Env{Session: testView(t, "S")}
	envOther := Env{Session: testView(t, "T")}
	out, err := tools["process_list"].Run(t.Context(), envOwner, json.RawMessage(`{}`))
	if err != nil || !out.OK || !contains(out.Content, info.ID) {
		t.Fatalf("owner list = %+v err=%v", out, err)
	}
	out, err = tools["process_list"].Run(t.Context(), envOther, json.RawMessage(`{}`))
	if err != nil || out.Content != "[]" {
		t.Fatalf("other list = %q err=%v", out.Content, err)
	}
	killArgs := json.RawMessage(`{"id":"` + info.ID + `","signal":"KILL"}`)
	if _, err := tools["process_kill"].Run(t.Context(), envOther, killArgs); codeOf(err) != CodeNotFound {
		t.Fatalf("foreign kill = %v", err)
	}
	if _, err := tools["process_output"].Run(t.Context(), envOther, json.RawMessage(`{"id":"`+info.ID+`"}`)); codeOf(err) != CodeNotFound {
		t.Fatalf("foreign output = %v", err)
	}
	if out, err := tools["process_kill"].Run(t.Context(), envOwner, killArgs); err != nil || out.OK != true {
		t.Fatalf("owner kill = %+v err=%v", out, err)
	}
}

func TestProcessToolsWaitTimeoutAndOutput(t *testing.T) {
	t.Parallel()
	reg := exec.NewBackground()
	t.Cleanup(func() {
		//nolint:errcheck,gosec // test cleanup
		reg.Close(context.Background())
	})
	tools := procTools(t, reg)
	info := startSleeper(t, reg, "S", "printf 'a\\nb\\n'; sleep 30")
	env := Env{Session: testView(t, "S")}
	deadline := time.Now().Add(2 * time.Second)
	for {
		out, err := tools["process_output"].Run(t.Context(), env, json.RawMessage(`{"id":"`+info.ID+`"}`))
		if err != nil {
			t.Fatalf("output: %v", err)
		}
		if strings.TrimRight(out.Content, "\n") == "a\nb" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("output = %q", out.Content)
		}
		<-time.After(10 * time.Millisecond)
	}
	start := time.Now()
	out, err := tools["process_wait"].Run(t.Context(), env, json.RawMessage(`{"id":"`+info.ID+`","timeout_s":1}`))
	if err != nil || out.Meta["status"] != "still_running" {
		t.Fatalf("wait = %+v err=%v", out, err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("wait hung %s", time.Since(start))
	}
	// Offset advances and tail/grep are honoured.
	out, err = tools["process_output"].Run(t.Context(), env, json.RawMessage(`{"id":"`+info.ID+`","grep":"a"}`))
	if err != nil || !contains(out.Content, "a") {
		t.Fatalf("grep = %+v err=%v", out, err)
	}
	if out.Meta["offset"] == int64(0) {
		t.Fatalf("offset = %v", out.Meta["offset"])
	}
}

func TestProcessToolsSendKillIdempotentAndApproval(t *testing.T) {
	t.Parallel()
	reg := exec.NewBackground()
	t.Cleanup(func() {
		//nolint:errcheck,gosec // test cleanup
		reg.Close(context.Background())
	})
	tools := procTools(t, reg)
	info := startSleeper(t, reg, "S", "read x; printf 'got:%s' \"$x\"")
	env := Env{Session: testView(t, "S")}
	if out, err := tools["process_send"].Run(t.Context(), env, json.RawMessage(`{"id":"`+info.ID+`","text":"hi"}`)); err != nil {
		t.Fatalf("send: %v out=%+v", err, out)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		out, _ := tools["process_wait"].Run(t.Context(), env, json.RawMessage(`{"id":"`+info.ID+`","timeout_s":1}`))
		if out.Meta["status"] != "still_running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no exit")
		}
	}
	if _, err := tools["process_send"].Run(t.Context(), env, json.RawMessage(`{"id":"`+info.ID+`","text":"x"}`)); err == nil {
		t.Fatal("write after exit accepted")
	}
	killArgs := json.RawMessage(`{"id":"` + info.ID + `","signal":"KILL"}`)
	if out, err := tools["process_kill"].Run(t.Context(), env, killArgs); err != nil || !out.OK {
		t.Fatalf("kill exited = %+v err=%v", out, err)
	}
	if out, err := tools["process_kill"].Run(t.Context(), env, killArgs); err != nil || !out.OK {
		t.Fatalf("kill idempotent = %+v err=%v", out, err)
	}
	// Approval: kill/send ask interactively, deny headless; list is auto.
	pc := PolicyContext{Mode: ModeAsk}
	for _, name := range []string{"process_kill", "process_send"} {
		d, err := tools[name].(Inspector).Inspect(t.Context(), env, json.RawMessage(`{"id":"`+info.ID+`","text":"secret","signal":"TERM"}`), pc)
		if err != nil || d.Requirement != RequireAsk || d.AllowSession {
			t.Fatalf("%s inspect = %+v err=%v", name, d, err)
		}
		if contains(d.Preview, "secret") {
			t.Fatalf("%s preview leaks text: %q", name, d.Preview)
		}
	}
	if d, err := tools["process_list"].(Inspector).Inspect(t.Context(), env, json.RawMessage(`{}`), pc); err != nil || d.Requirement != RequireAuto {
		t.Fatalf("list inspect = %+v err=%v", d, err)
	}
	hd := PolicyContext{Mode: ModeHeadless}
	if d, _ := tools["process_kill"].(Inspector).Inspect(t.Context(), env, killArgs, hd); d.Requirement != RequireDeny {
		t.Fatalf("headless kill = %+v", d)
	}
}

func TestProcessToolsCapAndClose(t *testing.T) {
	t.Parallel()
	reg := exec.NewBackground()
	defer func() {
		//nolint:errcheck,gosec // test cleanup
		reg.Close(context.Background())
	}()
	for range exec.MaxLivePerSession {
		startSleeper(t, reg, "S", "sleep 30")
	}
	if _, err := reg.Start(t.Context(), "S", []string{"/bin/sleep", "30"}, "/tmp", nil); !errors.Is(err, exec.ErrProcessLimit) {
		t.Fatalf("cap = %v", err)
	}
	if err := reg.Close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, _, err := reg.Wait(context.Background(), "S", "p1", time.Second); !errors.Is(err, exec.ErrProcessClosed) {
		t.Fatalf("after close = %v", err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
