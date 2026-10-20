package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/loop/event"
)

func TestWriteStreamEvent(t *testing.T) {
	var buf bytes.Buffer
	up := event.Update{Kind: event.UpdateTextDelta, Text: "hi"}
	if err := writeStreamEvent(&buf, &up); err != nil {
		t.Fatalf("writeStreamEvent: %v", err)
	}
	if got := buf.String(); !strings.Contains(got, `"hi"`) || !strings.Contains(got, `"type":"text_delta"`) {
		t.Fatalf("unexpected stream line: %q", got)
	}
}

func TestDrainRunAccumulates(t *testing.T) {
	ch := make(chan event.Update, 4)
	usage := event.Usage{Input: 1, CachedInput: 2, Output: 3, Iterations: 4}
	ch <- event.Update{Kind: event.UpdateTextDelta, Text: "a"}
	ch <- event.Update{Kind: event.UpdateTextDelta, Text: "b"}
	ch <- event.Update{Kind: event.UpdateUsage}
	ch <- event.Update{Kind: event.UpdateStop, Stop: event.StopEndTurn, Detail: event.DetailCompleted, Usage: &usage}
	close(ch)

	var buf bytes.Buffer
	r := drainRun(ch, "stream-json", &buf)
	if r.answer != "ab" || r.stop != event.StopEndTurn || r.detail != event.DetailCompleted {
		t.Fatalf("unexpected result: %+v", r)
	}
	if r.usage.Input != 1 || r.usage.CachedInput != 2 || r.usage.Output != 3 || r.usage.Iterations != 4 {
		t.Fatalf("unexpected usage: %+v", r.usage)
	}
	if strings.Count(buf.String(), "\n") != 4 {
		t.Fatalf("expected one line per update, got %q", buf.String())
	}
}

func TestDrainRunTextModeIsQuiet(t *testing.T) {
	ch := make(chan event.Update, 1)
	ch <- event.Update{Kind: event.UpdateTextDelta, Text: "quiet"}
	close(ch)

	var buf bytes.Buffer
	if r := drainRun(ch, "text", &buf); r.answer != "quiet" {
		t.Fatalf("unexpected result: %+v", r)
	}
	if buf.Len() != 0 {
		t.Fatalf("text mode must not stream: %q", buf.String())
	}
}

func TestWriteRunResultModes(t *testing.T) {
	r := runResult{answer: "ok", stop: event.StopEndTurn, detail: event.DetailCompleted, sessionID: "s1", usage: event.Usage{Input: 2}}
	for _, mode := range []string{"text", "json", "stream-json"} {
		var buf bytes.Buffer
		if code := writeRunResult(&r, mode, &buf); code != 0 {
			t.Fatalf("%s: exit code %d", mode, code)
		}
		if buf.Len() == 0 {
			t.Fatalf("%s: empty output", mode)
		}
	}

	var buf bytes.Buffer
	if code := writeRunResult(&runResult{stop: event.StopMaxTokens}, "text", &buf); code != 2 {
		t.Fatalf("unfinished turn: exit code %d", code)
	}
}

func TestEncodeLineWrapsWriteError(t *testing.T) {
	if err := encodeLine(failingWriter{}, struct{}{}); err == nil {
		t.Fatal("expected a wrapped write error")
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("boom") }
