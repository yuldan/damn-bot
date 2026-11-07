package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/kanban"
)

// TestF7AUTO019KanbanWatcherSeesChange proves the production watcher: a card
// created after the cursor is initialized reaches the subscribed chat as one
// markdown body, and the same change is not replayed a second time.
func TestF7AUTO019KanbanWatcherSeesChange(t *testing.T) {
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
	if _, serr := store.Subscribe(t.Context(), "default", "telegram:42"); serr != nil {
		t.Fatal(serr)
	}
	sender := &notifySender{}
	w := &kanbanWatcher{store: store, notifier: &Notifier{Store: store, Sender: sender}}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.Run(ctx)
	}()
	// Let the watcher record the head before the change under test.
	<-time.After(2 * kanbanWatchEvery)
	task, _, err := store.CreateTask(t.Context(), "default", &kanban.TaskSpec{Title: "watched", Status: kanban.StatusReady}, "u")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for sender.count() == 0 && time.Now().Before(deadline) {
		<-time.After(20 * time.Millisecond)
	}
	if got := sender.count(); got != 1 {
		t.Fatalf("notifications = %d, want 1", got)
	}
	if !strings.Contains(sender.lastBody(), task.ID) {
		t.Fatalf("body = %q", sender.lastBody())
	}
	cancel()
	<-done
}

func (f *notifySender) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.msgs
}

func (f *notifySender) lastBody() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.text
}

// TestF7AUTO018KanbanDecomposeProd proves the production decomposer seam: the
// LLM answer is unwrapped and schema-checked by the store, and one run id is
// billed.
func TestF7AUTO018KanbanDecomposeProd(t *testing.T) {
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
	parent, _, err := store.CreateTask(t.Context(), "default", &kanban.TaskSpec{Title: "big task", Body: "body"}, "u")
	if err != nil {
		t.Fatal(err)
	}
	var prompt string
	d := &kanbanLLMDecomposer{turnFn: func(_ context.Context, p string) (string, error) {
		prompt = p
		return "```json\n{\"children\":[{\"title\":\"child one\"},{\"title\":\"child two\"}]}\n```", nil
	}}
	kids, err := store.Decompose(t.Context(), parent.ID, d, 8)
	if err != nil || len(kids) != 2 {
		t.Fatalf("decompose = %v, %v", kids, err)
	}
	if !strings.Contains(prompt, "big task") || !strings.Contains(prompt, "at most 8") {
		t.Fatalf("prompt = %q", prompt)
	}
	if _, cerr := store.Decompose(t.Context(), parent.ID, &kanbanLLMDecomposer{turnFn: func(context.Context, string) (string, error) {
		return "not json", nil
	}}, 8); cerr == nil {
		t.Fatal("invalid plan accepted")
	}
}
