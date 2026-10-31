package loop

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/damnbot/damnbot/internal/loop/event"
)

// gateWriter holds the "panic recovered" log line until the test releases
// it. That makes the ordering observable without sleeping: once logged is
// closed, recovery has started in the turn goroutine, and the stream must
// still be open because the turn may only close it after the log write
// returns.
type gateWriter struct {
	logged  chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *gateWriter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("panic recovered")) {
		w.once.Do(func() { close(w.logged) })
		<-w.release
	}
	return len(p), nil
}

// TestPromptClosesAfterPanicRecovery (E-29): a consumer that sees the stream
// close must never observe a turn whose panic is not recovered and logged
// yet. The bug this pins: recover+ERROR log ran outside the turn goroutine,
// after close(out); a slow first log (Windows time zone init) then outlived
// the stream, so goleak reported the goroutine as leaked.
func TestPromptClosesAfterPanicRecovery(t *testing.T) {
	const rounds = 200
	for range rounds {
		checkCloseAfterPanic(t)
	}
}

func checkCloseAfterPanic(t *testing.T) {
	t.Helper()
	gate := &gateWriter{logged: make(chan struct{}), release: make(chan struct{})}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(gate, nil)))
	defer slog.SetDefault(prev)
	defer func() {
		select {
		case <-gate.release:
		default:
			close(gate.release) // never leave the recovery blocked on a failure
		}
	}()

	f := newFixture()
	f.provider.scripts = []streamFunc{func(context.Context, *Request, func(Delta)) (Reply, error) {
		panic("boom") //nolint:forbidigo // provokes the panic boundary this test asserts.
	}}
	deps := f.deps()
	l, err := New(&f.cfg, &deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	out, err := l.Prompt(t.Context(), "S1", Input{Text: "hello"})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	<-gate.logged // recovery is running and blocked in the log handler
	if early := streamClosed(out); early {
		t.Error("stream closed before panic recovery/logging finished")
	}
	close(gate.release)
	for range out { // drain: the stream must close on its own
	}

	if _, ok := <-out; ok {
		t.Error("stream delivered an update after close")
	}
	if l.Active("S1") {
		t.Error("turn still registered as active after the stream closed")
	}
	if n := f.store.countKind(event.TurnEnd); n != 1 {
		t.Fatalf("turn_end rows = %d, want exactly 1 (%v)", n, f.store.kinds())
	}
	ev, ok := f.store.find(event.TurnEnd)
	if !ok {
		t.Fatal("turn_end row missing")
	}
	var p endPayload
	payloadOf(t, &ev, &p)
	if p.Stop != string(event.StopError) || p.Detail != string(event.DetailInternal) || p.Error != "panic" {
		t.Fatalf("panic terminal = %+v, want error/internal/panic", p)
	}
}

// streamClosed reports whether ch is already closed, without consuming a
// value or waiting.
func streamClosed(ch <-chan event.Update) bool {
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return true
			}
		default:
			return false
		}
	}
}
