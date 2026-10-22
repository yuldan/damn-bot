package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// TestRunDeniesDamnbotMaintenance (SEC15-2): the real `run` entry point
// refuses destructive damnbot maintenance under --unsafe-auto-shell.
func TestRunDeniesDamnbotMaintenance(t *testing.T) {
	t.Parallel()
	call := `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"shell","arguments":"{\"command\":\"damnbot sessions purge s --yes\"}"}}]}}]}`
	end := `{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`
	srv := providertest.New(t, nil,
		providertest.Reply{Frames: []string{call, end, "[DONE]"}},
		providertest.Text("stopped"),
	)
	var out, errb bytes.Buffer
	code := run([]string{
		"run", "-p", "purge", "--model", "openai/gpt-fake", "--base-url", srv.URL,
		"--data-dir", t.TempDir(), "--output", "json", "--approval", "auto",
		"--unsafe-auto-shell", "--provider-attempts", "1",
	}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s, stdout = %s", code, errb.String(), out.String())
	}
	reqs := srv.Requests()
	if len(reqs) != 2 || !strings.Contains(string(reqs[1]), "denied") {
		t.Fatalf("maintenance was not denied: %q", string(reqs[len(reqs)-1]))
	}
}
