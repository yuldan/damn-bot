package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// TestRunDamnbotMaintenanceGate (SEC15-2/5/8): the real `run` entry point
// auto-runs a benign shell command but denies destructive damnbot maintenance,
// including argv0-hiding wrappers.
func TestRunDamnbotMaintenanceGate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		command string
		denied  bool
	}{
		{"positive-control", "echo ok", false},
		{"purge", "damnbot sessions purge s --yes", true},
		{"exec-argv0", "exec -a x damnbot sessions purge s --yes", true},
		{"env-argv0", "env -a x damnbot sessions purge s --yes", true},
		{"env-argv0-long", "env --argv0 x damnbot sessions purge s --yes", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args, err := json.Marshal(map[string]string{"command": tc.command})
			if err != nil {
				t.Fatal(err)
			}
			call := `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"shell","arguments":` +
				jsonString(string(args)) + `}}]}}]}`
			end := `{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`
			srv := providertest.New(t, nil,
				providertest.Reply{Frames: []string{call, end, "[DONE]"}},
				providertest.Text("stopped"),
			)
			var out, errb bytes.Buffer
			code := run([]string{
				"run", "-p", "maintenance", "--model", "openai/gpt-fake", "--base-url", srv.URL,
				"--data-dir", t.TempDir(), "--output", "json", "--approval", "auto",
				"--unsafe-auto-shell", "--provider-attempts", "1",
			}, &out, &errb)
			if code != 0 {
				t.Fatalf("exit = %d, stderr = %s, stdout = %s", code, errb.String(), out.String())
			}
			reqs := srv.Requests()
			if len(reqs) != 2 {
				t.Fatalf("provider calls = %d, want 2", len(reqs))
			}
			tool := string(reqs[1])
			if got := strings.Contains(tool, "denied"); got != tc.denied {
				t.Fatalf("denied = %v, want %v: %s", got, tc.denied, tool)
			}
			if !tc.denied && !strings.Contains(tool, "ok") {
				t.Fatalf("positive control did not run: %s", tool)
			}
		})
	}
}
