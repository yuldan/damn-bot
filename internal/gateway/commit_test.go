package gateway_test

import (
	"context"
	"errors"
	"testing"

	"github.com/damnbot/damnbot/internal/gateway"
)

// commitRecorder collects every commit signal of one inbound.
type commitRecorder struct{ signals []error }

func (r *commitRecorder) commit(err error) { r.signals = append(r.signals, err) }

func TestAdmitCommitSignalPrecedesWork(t *testing.T) {
	ids := map[string]gateway.Identity{"telegram:7": owner("7")}
	g, _ := newGate(t, ids, nil, "")
	in := inbound("c1", "7", "u:1")
	rec := &commitRecorder{}
	in.Committed = rec.commit
	ran := false
	work := gateway.RunFunc(func(context.Context, gateway.Ticket) error {
		ran = true
		if len(rec.signals) != 1 || rec.signals[0] != nil {
			t.Errorf("work started before commit signal: %v", rec.signals)
		}
		return nil
	})
	out, err := g.Admit(t.Context(), in, work)
	if err != nil || out.Kind != gateway.Accepted {
		t.Fatalf("outcome = %+v err = %v, want accepted", out, err)
	}
	if !ran || len(rec.signals) != 1 || rec.signals[0] != nil {
		t.Fatalf("ran = %t signals = %v, want one nil commit", ran, rec.signals)
	}
}

func TestAdmitCommitSignalOnDenial(t *testing.T) {
	g, inbox := newGate(t, nil, nil, "")
	in := inbound("c1", "stranger", "u:1")
	rec := &commitRecorder{}
	in.Committed = rec.commit
	work := gateway.RunFunc(func(context.Context, gateway.Ticket) error {
		t.Error("denied update reached work")
		return nil
	})
	out, err := g.Admit(t.Context(), in, work)
	if err != nil || out.Kind != gateway.Rejected {
		t.Fatalf("outcome = %+v err = %v, want rejected", out, err)
	}
	if inbox.calls != 0 {
		t.Errorf("inbox writes = %d, want 0", inbox.calls)
	}
	// A denied update has no durable work to lose, so the transport may ack it.
	if len(rec.signals) != 1 || rec.signals[0] != nil {
		t.Fatalf("signals = %v, want one nil", rec.signals)
	}
}

func TestAdmitCommitSignalOnInboxFailure(t *testing.T) {
	ids := map[string]gateway.Identity{"telegram:7": owner("7")}
	g, inbox := newGate(t, ids, nil, "")
	inbox.err = errors.New("disk full")
	in := inbound("c1", "7", "u:1")
	rec := &commitRecorder{}
	in.Committed = rec.commit
	work := gateway.RunFunc(func(context.Context, gateway.Ticket) error {
		t.Error("work ran after a failed inbox write")
		return nil
	})
	if _, err := g.Admit(t.Context(), in, work); err == nil {
		t.Fatal("Admit error = nil, want inbox failure")
	}
	if len(rec.signals) != 1 || rec.signals[0] == nil {
		t.Fatalf("signals = %v, want one non-nil error", rec.signals)
	}
	if inbox.calls != 1 {
		t.Errorf("inbox writes = %d, want 1", inbox.calls)
	}
}

func TestAdmitCommittedDuplicateDoesNotRunTwice(t *testing.T) {
	ids := map[string]gateway.Identity{"telegram:7": owner("7")}
	g, _ := newGate(t, ids, nil, "")
	first := inbound("c1", "7", "u:1")
	first.Committed = (&commitRecorder{}).commit
	runs := 0
	work := gateway.RunFunc(func(context.Context, gateway.Ticket) error {
		runs++
		return nil
	})
	if out, err := g.Admit(t.Context(), first, work); err != nil || out.Kind != gateway.Accepted {
		t.Fatalf("first outcome = %+v err = %v", out, err)
	}
	// The process died after commit and before the turn, then Telegram retried
	// the same update: the durable row still wins and no second turn runs.
	second := inbound("c1", "7", "u:1")
	rec := &commitRecorder{}
	second.Committed = rec.commit
	out, err := g.Admit(t.Context(), second, work)
	if err != nil || out.Kind != gateway.Duplicate {
		t.Fatalf("retry outcome = %+v err = %v, want duplicate", out, err)
	}
	if runs != 1 {
		t.Fatalf("turns = %d, want 1", runs)
	}
	if len(rec.signals) != 1 || rec.signals[0] != nil {
		t.Fatalf("retry signals = %v, want one nil", rec.signals)
	}
}

func TestAdmitPostCommitWorkErrorKeepsCommit(t *testing.T) {
	ids := map[string]gateway.Identity{"telegram:7": owner("7")}
	g, _ := newGate(t, ids, nil, "")
	in := inbound("c1", "7", "u:1")
	rec := &commitRecorder{}
	in.Committed = rec.commit
	work := gateway.RunFunc(func(context.Context, gateway.Ticket) error {
		return errors.New("turn failed")
	})
	if _, err := g.Admit(t.Context(), in, work); err == nil {
		t.Fatal("Admit error = nil, want turn failure")
	}
	// The row is durable, so the transport must not turn a turn failure into a
	// retryable non-2xx; the commit signal stays the one nil.
	if len(rec.signals) != 1 || rec.signals[0] != nil {
		t.Fatalf("signals = %v, want one nil", rec.signals)
	}
}
