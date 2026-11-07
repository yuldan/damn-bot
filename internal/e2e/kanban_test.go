package e2e

import (
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// TestF7KanbanCLI drives the real binary through a board mutation and a
// read: init, create, list and an unknown subcommand. It is the end-to-end
// proof that the CLI reaches the ctxdb board (CLI-029) rather than a stub.
func TestF7KanbanCLI(t *testing.T) {
	t.Parallel()
	e := New(t, providertest.Text("unused"))
	args := func(sub ...string) []string {
		return append(append([]string{"kanban"}, sub...), "--data-dir", e.DataDir, "--config", e.cfg, "--json")
	}
	if out := e.Exec(args("init", "--name", "default")...); out.Code != 0 {
		t.Fatalf("init: code %d\n%s%s", out.Code, out.Stdout, out.Stderr)
	}
	created := e.Exec(args("create", "--title", "e2e task", "--status", "ready")...)
	if created.Code != 0 || !strings.Contains(created.Stdout, `"created": true`) {
		t.Fatalf("create: code %d\n%s%s", created.Code, created.Stdout, created.Stderr)
	}
	list := e.Exec(args("list")...)
	if list.Code != 0 || !strings.Contains(list.Stdout, "e2e task") {
		t.Fatalf("list: code %d\n%s%s", list.Code, list.Stdout, list.Stderr)
	}
	if bad := e.Exec(args("nonesuch")...); bad.Code != 2 {
		t.Fatalf("unknown subcommand exit = %d", bad.Code)
	}
}
