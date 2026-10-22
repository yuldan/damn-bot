package e2e

import (
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// TestContextRefFile: @file content reaches the provider inside an untrusted
// boundary (MEM-024).
func TestContextRefFile(t *testing.T) {
	t.Parallel()
	e := New(t, providertest.Text("ok"))
	e.WriteFile("notes.txt", "deploy on friday")
	e.Run("summarize @notes.txt").RequireOK(t)
	body := e.RequestText(t, 0)
	for _, want := range []string{`untrusted source=`, "deploy on friday"} {
		if !strings.Contains(body, want) {
			t.Errorf("request lacks %q:\n%s", want, body)
		}
	}
}

// TestContextRefOutsideWorkspace: an escaping reference fails the run closed.
func TestContextRefOutsideWorkspace(t *testing.T) {
	t.Parallel()
	e := New(t, providertest.Text("ok"))
	res := e.Run("read @../outside.txt")
	if res.Code != 2 || !strings.Contains(res.Stderr, "context refs") {
		t.Fatalf("exit = %d stderr = %q, want 2 with context refs error", res.Code, res.Stderr)
	}
	if got := len(e.Requests()); got != 0 {
		t.Fatalf("provider calls = %d, want 0", got)
	}
}
