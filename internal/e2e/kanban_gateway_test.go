//go:build unix

package e2e

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// kanbanConfig appends the opt-in [kanban] section to the scenario config.
func kanbanConfig(t *testing.T, e *Env, dispatch, decompose, notify bool) {
	t.Helper()
	f, err := os.OpenFile(e.cfg, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			t.Errorf("close config: %v", cerr)
		}
	}()
	section := fmt.Sprintf("\n[kanban]\ndispatch_in_gateway = %t\nauto_decompose = %t\nnotify_in_gateway = %t\n",
		dispatch, decompose, notify)
	if _, werr := f.WriteString(section); werr != nil {
		t.Fatal(werr)
	}
}

// kanbanCLI runs one board command in the scenario workspace, the same
// workspace the serve child uses for its notify file.
func kanbanCLI(t *testing.T, e *Env, args ...string) Result {
	t.Helper()
	return e.Exec(append(append([]string{"kanban"}, args...),
		"--data-dir", e.DataDir, "--config", e.cfg, "--json")...)
}

func kanbanSeed(t *testing.T, e *Env, title string, sub bool) string {
	t.Helper()
	if out := kanbanCLI(t, e, "init", "--name", "default"); out.Code != 0 {
		t.Fatalf("init: code %d\n%s%s", out.Code, out.Stdout, out.Stderr)
	}
	if sub {
		if out := kanbanCLI(t, e, "notify-subscribe", "--target", fmt.Sprintf("telegram:%d", gatewayOwner)); out.Code != 0 {
			t.Fatalf("notify-subscribe: code %d\n%s%s", out.Code, out.Stdout, out.Stderr)
		}
	}
	created := kanbanCLI(t, e, "create", "--title", title, "--status", "ready")
	if created.Code != 0 || !strings.Contains(created.Stdout, `"created": true`) {
		t.Fatalf("create: code %d\n%s%s", created.Code, created.Stdout, created.Stderr)
	}
	id := kanbanTaskID(t, created.Stdout)
	return id
}

// kanbanTaskID reads the id out of the create JSON without a schema import:
// the field is quoted exactly once at the head of the task object.
func kanbanTaskID(t *testing.T, out string) string {
	t.Helper()
	const marker = `"id": "`
	_, rest, ok := strings.Cut(out, marker)
	if !ok {
		t.Fatalf("no task id in %q", out)
	}
	id, _, ok := strings.Cut(rest, `"`)
	if !ok {
		t.Fatalf("unterminated task id in %q", out)
	}
	return id
}

// TestF7KanbanGatewayNotifyE2E proves the production path of AUTO-019: a task
// change seen by the gateway watcher reaches the subscribed chat through the
// durable outbox and the platform renderer, not through a fmt send.
func TestF7KanbanGatewayNotifyE2E(t *testing.T) {
	e := New(t, providertest.Text("unused"))
	kanbanConfig(t, e, false, false, true)
	kanbanSeed(t, e, "seeded", true)
	tg := NewFakeTG(t, FakeTGOptions{})
	p := startGatewayServe(t, e, tg)
	waitFor(t, 10*time.Second, "gateway poll", func() bool { return len(tg.RequestsFor("getUpdates")) > 0 })

	if out := kanbanCLI(t, e, "create", "--title", "notify **me**", "--status", "ready"); out.Code != 0 {
		t.Fatalf("create: code %d\n%s%s", out.Code, out.Stdout, out.Stderr)
	}
	waitFor(t, 15*time.Second, "kanban notice", func() bool {
		for _, s := range tg.Sends() {
			if strings.Contains(s.Text, "kanban") && strings.Contains(s.Text, "notify me") {
				return true
			}
		}
		return false
	})
	gatewayStop(t, p)
}

// TestF7KanbanGatewayDispatchE2E proves AUTO-017: with dispatch_in_gateway on,
// the gateway tick claims a ready card and runs it, and the board records it
// done. AUTO-017 off is the negative control in TestF7KanbanGatewayOffE2E.
func TestF7KanbanGatewayDispatchE2E(t *testing.T) {
	e := New(t, providertest.Text("worker finished the card"))
	kanbanConfig(t, e, true, false, false)
	taskID := kanbanSeed(t, e, "dispatch me", false)
	tg := NewFakeTG(t, FakeTGOptions{})
	p := startGatewayServe(t, e, tg)
	waitFor(t, 25*time.Second, "task done", func() bool {
		out := kanbanCLI(t, e, "show", taskID)
		return out.Code == 0 && strings.Contains(out.Stdout, `"status": "done"`)
	})
	gatewayStop(t, p)
}

// TestF7KanbanGatewayOffE2E is the negative control: with every [kanban]
// switch off the gateway neither dispatches the ready card nor notifies.
func TestF7KanbanGatewayOffE2E(t *testing.T) {
	e := New(t, providertest.Text("unused"))
	kanbanConfig(t, e, false, false, false)
	taskID := kanbanSeed(t, e, "off control", true)
	tg := NewFakeTG(t, FakeTGOptions{})
	p := startGatewayServe(t, e, tg)
	waitFor(t, 10*time.Second, "gateway poll", func() bool { return len(tg.RequestsFor("getUpdates")) > 0 })
	<-time.After(7 * time.Second) // > dispatch tick and > watcher tick
	if out := kanbanCLI(t, e, "show", taskID); out.Code != 0 || !strings.Contains(out.Stdout, `"status": "ready"`) {
		t.Fatalf("off control changed the card: code %d\n%s%s", out.Code, out.Stdout, out.Stderr)
	}
	if sends := tg.Sends(); len(sends) != 0 {
		t.Fatalf("off control sent %d messages: %+v", len(sends), sends)
	}
	gatewayStop(t, p)
}
