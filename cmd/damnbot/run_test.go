package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// TestRunHeadlessFakeProvider is the F1 `damnbot run` smoke: one loop turn
// against the loopback fake provider, JSON result on stdout, exit 0.
func TestRunHeadlessFakeProvider(t *testing.T) {
	t.Parallel()
	srv := providertest.New(t, nil, providertest.Text("hello from the fake provider"))
	var out, errb bytes.Buffer
	code := run([]string{
		"run", "-p", "say hello",
		"--model", "openai/gpt-fake",
		"--base-url", srv.URL,
		"--data-dir", t.TempDir(),
		"--output", "json",
	}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s, stdout = %s", code, errb.String(), out.String())
	}
	var got struct {
		Schema     string `json:"schema"`
		StopReason string `json:"stop_reason"`
		StopDetail string `json:"stop_detail"`
		Answer     string `json:"answer"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("json output: %v\n%s", err, out.String())
	}
	if got.Schema != "damnbot.turn_result/v1" || got.StopReason != "end_turn" || got.StopDetail != "completed" {
		t.Errorf("result = %+v", got)
	}
	if !strings.Contains(got.Answer, "hello from the fake provider") {
		t.Errorf("answer = %q", got.Answer)
	}
	if len(srv.Requests()) != 1 {
		t.Errorf("provider calls = %d, want 1", len(srv.Requests()))
	}
}
