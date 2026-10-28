package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

type queueRunner struct {
	prompts chan string
	cancels int
	active  bool
}

func newQueueRunner() *queueRunner { return &queueRunner{prompts: make(chan string, 4)} }

func (r *queueRunner) Prompt(_ context.Context, _ string, in Input) (<-chan Update, error) {
	r.active = true
	r.prompts <- in.Text
	ch := make(chan Update)
	close(ch)
	return ch, nil
}
func (r *queueRunner) Cancel(string)      { r.cancels++ }
func (r *queueRunner) Active(string) bool { return false }

func newQueueApp(t *testing.T) (*Model, *queueRunner) {
	t.Helper()
	runner := newQueueRunner()
	m, err := NewApp("sess-q", testHost(newFakeClock(), runner, &fakeConfig{cfg: Config{Model: "fake/model"}}))
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	return m, runner
}

// TestQueueFIFONotDropped: input submitted during a turn is queued, shown, and
// fed to the Runner in order after finishTurn.
func TestQueueFIFONotDropped(t *testing.T) {
	t.Parallel()
	m, runner := newQueueApp(t)
	m.busy = true
	for _, text := range []string{"one", "two"} {
		m.input.SetValue(text)
		if cmd := m.submit(); cmd != nil {
			t.Fatalf("submit during a turn started a command")
		}
	}
	if len(m.queue) != 2 || !strings.Contains(m.View().Content, "queue 2") {
		t.Fatalf("queue = %v, view lacks indicator", m.queue)
	}
	cmd := m.finishTurn()
	if cmd == nil {
		t.Fatalf("finishTurn did not dequeue")
	}
	cmd()
	if got := <-runner.prompts; got != "one" {
		t.Fatalf("first dequeued = %q", got)
	}
	if cmd := m.finishTurn(); cmd == nil {
		t.Fatalf("second finishTurn did not dequeue")
	} else {
		cmd()
	}
	if got := <-runner.prompts; got != "two" {
		t.Fatalf("second dequeued = %q", got)
	}
	if len(m.queue) != 0 {
		t.Fatalf("queue still holds %v", m.queue)
	}
}

func TestQueueCancelClears(t *testing.T) {
	t.Parallel()
	m, _ := newQueueApp(t)
	m.busy = true
	m.input.SetValue("one")
	m.submit()
	m.input.SetValue("two")
	m.submit()
	m.Update(navKey(tea.KeyEsc, 0))
	if len(m.queue) != 0 {
		t.Fatalf("esc left %v queued", m.queue)
	}
	m.busy = true
	m.input.SetValue("three")
	m.submit()
	m.handleKey(navKey('c', tea.ModCtrl))
	if len(m.queue) != 0 {
		t.Fatalf("ctrl+c left %v queued", m.queue)
	}
}

func TestQueueCommands(t *testing.T) {
	t.Parallel()
	m, _ := newQueueApp(t)
	m.busy = true
	m.input.SetValue("one")
	m.submit()
	m.input.SetValue("two")
	m.submit()
	runSlashText(t, m, "/queue list")
	if !strings.Contains(lastNotice(m), "queue (2)") {
		t.Fatalf("list = %q", lastNotice(m))
	}
	runSlashText(t, m, "/queue add three")
	if got := strings.Join(m.queue, ","); got != "one,two,three" {
		t.Fatalf("after add = %q", got)
	}
	runSlashText(t, m, "/queue move 1 3")
	if got := strings.Join(m.queue, ","); got != "two,three,one" {
		t.Fatalf("after move = %q", got)
	}
	runSlashText(t, m, "/queue edit 2 baru")
	if got := strings.Join(m.queue, ","); got != "two,baru,one" {
		t.Fatalf("after edit = %q", got)
	}
	runSlashText(t, m, "/queue rm 1")
	if got := strings.Join(m.queue, ","); got != "baru,one" {
		t.Fatalf("after rm = %q", got)
	}
	runSlashText(t, m, "/queue clear")
	if len(m.queue) != 0 {
		t.Fatalf("clear left %v", m.queue)
	}
}

func TestSteerAndBusyRejected(t *testing.T) {
	t.Parallel()
	m, runner := newQueueApp(t)
	m.busy = true
	m.queue = []string{"keep"}
	runSlashText(t, m, "/steer hello")
	if !strings.Contains(lastNotice(m), "steer refused") || len(m.queue) != 1 || !m.busy {
		t.Fatalf("steer changed state: %q busy=%v queue=%v", lastNotice(m), m.busy, m.queue)
	}
	if runner.cancels != 0 {
		t.Fatalf("steer called Cancel")
	}
	runSlashText(t, m, "/busy status")
	if !strings.Contains(lastNotice(m), "busy mode queue; queue 1") {
		t.Fatalf("busy status = %q", lastNotice(m))
	}
	runSlashText(t, m, "/busy steer")
	if !strings.Contains(lastNotice(m), "busy steer refused") {
		t.Fatalf("busy steer = %q", lastNotice(m))
	}
	runSlashText(t, m, "/busy interrupt")
	if !strings.Contains(lastNotice(m), "busy interrupt refused") {
		t.Fatalf("busy interrupt = %q", lastNotice(m))
	}
}
