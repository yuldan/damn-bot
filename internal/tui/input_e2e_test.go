package tui

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"
)

// scriptedRunner is the in-process Runner of the input e2e: Prompt announces
// the call, then relays whatever the test feeds through updates; no network,
// provider, or loop is involved.
type scriptedRunner struct {
	prompts chan Input
	updates chan Update
	cancels atomic.Int64
	active  atomic.Bool
}

func newScriptedRunner() *scriptedRunner {
	return &scriptedRunner{prompts: make(chan Input, 4), updates: make(chan Update, 8)}
}

// Prompt implements Runner.
func (r *scriptedRunner) Prompt(ctx context.Context, _ string, in Input) (<-chan Update, error) {
	r.active.Store(true)
	r.prompts <- in
	out := make(chan Update, 8)
	go func() {
		defer close(out)
		defer r.active.Store(false)
		for {
			select {
			case u, ok := <-r.updates:
				if !ok {
					return
				}
				out <- u
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// Cancel implements Runner.
func (r *scriptedRunner) Cancel(string) { r.cancels.Add(1) }

// Active implements Runner.
func (r *scriptedRunner) Active(string) bool { return r.active.Load() }

// newScriptedApp assembles a model over the scripted runner.
func newScriptedApp(t *testing.T) (*Model, *fakeClock, *scriptedRunner) {
	t.Helper()
	clk, runner := newFakeClock(), newScriptedRunner()
	m, err := NewApp("sess-e2e", Host{Clock: clk, Runner: runner, Config: fakeConfig{cfg: Config{Model: "fake/model"}}})
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	return m, clk, runner
}

// recvInput blocks until the Runner sees a Prompt.
func recvInput(t *testing.T, ch <-chan Input) Input {
	t.Helper()
	select {
	case in := <-ch:
		return in
	case <-time.After(2 * time.Second):
		t.Fatal("Runner.Prompt was not called")
		return Input{}
	}
}

// TestTeatestInputRoundTrip is the T1 acceptance path: the program starts, the
// user types, enter submits to the Runner, the stream update renders as a
// timestamped block, and ctrl+c leaves.
func TestTeatestInputRoundTrip(t *testing.T) {
	m, clk, runner := newScriptedApp(t)
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 24))
	tm.Type("halo dunia")
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})

	in := recvInput(t, runner.prompts)
	if in.Text != "halo dunia" {
		t.Fatalf("Prompt input = %q, want %q", in.Text, "halo dunia")
	}

	at := clk.Now()
	runner.updates <- Update{Kind: KindTextDelta, Text: "balasan-uji", At: at}
	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return bytes.Contains(out, []byte("balasan-uji"))
	}, teatest.WithDuration(2*time.Second), teatest.WithCheckInterval(10*time.Millisecond))

	runner.updates <- Update{Kind: KindStop, Stop: "end_turn", Detail: "completed", At: at}
	close(runner.updates)
	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return bytes.Contains(out, []byte("end_turn"))
	}, teatest.WithDuration(2*time.Second), teatest.WithCheckInterval(10*time.Millisecond))

	tm.Send(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))

	final, ok := tm.FinalModel(t).(*Model)
	if !ok {
		t.Fatalf("final model is not *Model")
	}
	if blocks := final.Blocks(); len(blocks) < 2 || blocks[0].Kind != BlockUser || blocks[1].Kind != BlockAssistant {
		t.Fatalf("transcript = %+v, want user then assistant", blocks)
	}
	if got := final.RenderedAt(); got.Before(at) {
		t.Fatalf("renderedAt %v before Update.At %v", got, at)
	}
	if final.busy {
		t.Fatalf("turn still busy after the Runner closed its stream")
	}
}

// TestInputNewlineAndCancel pins the remaining keymap: ctrl+j inserts into the
// prompt instead of submitting, and esc cancels a live turn through the Runner.
func TestInputNewlineAndCancel(t *testing.T) {
	t.Parallel()
	m, _, runner := newScriptedApp(t)
	m.input.SetValue("baris satu")
	m.Update(tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl})
	if got := m.input.Value(); got != "" {
		t.Fatalf("ctrl+j kept %q on the active line", got)
	}
	if len(m.draft) != 1 || m.draft[0] != "baris satu" {
		t.Fatalf("ctrl+j draft = %q, want the committed line", m.draft)
	}
	if n := len(runner.prompts); n != 0 {
		t.Fatalf("ctrl+j submitted %d prompts", n)
	}

	m.input.SetValue("baris dua")
	cmd := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatalf("enter returned no command")
	}
	cmd()
	if in := recvInput(t, runner.prompts); in.Text != "baris satu\nbaris dua" {
		t.Fatalf("Prompt input = %q, want a two-line prompt", in.Text)
	}
	if !runner.Active("sess-e2e") {
		t.Fatalf("Runner is not active after submit")
	}
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	if got := runner.cancels.Load(); got != 1 {
		t.Fatalf("esc called Cancel %d times, want 1", got)
	}
	m.finishTurn() // stop the scripted pump goroutine before goleak runs
}
