package tui

import (
	"bytes"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"
)

// TestTeatestQueueSurvivesTurn drives a real bubbletea program: a second prompt
// typed while the first turn is active is queued (not lost and not sent), then
// reaches the Runner as the next turn after the stream closes.
func TestTeatestQueueSurvivesTurn(t *testing.T) {
	m, clk, runner := newScriptedApp(t)
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 24))
	tm.Type("first")
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	if in := recvInput(t, runner.prompts); in.Text != "first" {
		t.Fatalf("first prompt = %q", in.Text)
	}

	tm.Type("second")
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return bytes.Contains(out, []byte("queue 1"))
	}, teatest.WithDuration(2*time.Second), teatest.WithCheckInterval(10*time.Millisecond))
	select {
	case got := <-runner.prompts:
		t.Fatalf("queued prompt started early: %q", got.Text)
	default:
	}

	at := clk.Now()
	runner.updates <- Update{Kind: KindTextDelta, Text: "answer", At: at}
	runner.updates <- Update{Kind: KindStop, Stop: "end_turn", Detail: "completed", At: at}
	close(runner.updates)

	if in := recvInput(t, runner.prompts); in.Text != "second" {
		t.Fatalf("dequeued prompt = %q, want second", in.Text)
	}
	tm.Send(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))

	final, ok := tm.FinalModel(t).(*Model)
	if !ok {
		t.Fatalf("final model is not *Model")
	}
	if len(final.queue) != 0 {
		t.Fatalf("queue still holds %v", final.queue)
	}
	var users []string
	for _, b := range final.Blocks() {
		if b.Kind == BlockUser {
			users = append(users, b.Text)
		}
	}
	if len(users) != 2 || users[0] != "first" || users[1] != "second" {
		t.Fatalf("transcript lost a queued prompt: %q", users)
	}
}
