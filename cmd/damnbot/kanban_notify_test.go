package main

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/gateway/render"
	"github.com/damnbot/damnbot/internal/kanban"
)

type notifySender struct {
	mu   sync.Mutex
	msgs int
	text string
}

func (f *notifySender) Send(_ context.Context, _, _ string, msgs []render.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs++
	if len(msgs) > 0 {
		f.text = msgs[0].Text
	}
	return nil
}

// TestF7AUTO019KanbanNotify verifies the rendered, rate-limited watcher.
func TestF7AUTO019KanbanNotify(t *testing.T) {
	t.Parallel()
	db, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: t.TempDir(), Kind: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	})
	store := kanban.New(db, t.TempDir())
	if _, ierr := store.InitBoard(t.Context(), "default", "u"); ierr != nil {
		t.Fatal(ierr)
	}
	task, _, err := store.CreateTask(t.Context(), "default", &kanban.TaskSpec{Title: "notify **me**", Status: kanban.StatusReady}, "u")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Subscribe(t.Context(), "default", "telegram:42"); err != nil {
		t.Fatal(err)
	}
	sender := &notifySender{}
	n := &Notifier{Store: store, Sender: sender}
	if err := n.Notify(t.Context(), task, "moved"); err != nil {
		t.Fatal(err)
	}
	if sender.msgs != 1 || strings.Contains(sender.text, "**") {
		t.Fatalf("msgs=%d text=%q", sender.msgs, sender.text)
	}
	if err := n.Notify(t.Context(), task, "again"); err != nil {
		t.Fatal(err)
	}
	if sender.msgs != 1 {
		t.Fatalf("rate limit: %d", sender.msgs)
	}
}
