package gwpeer

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

func TestF7GW023ChannelDirectory(t *testing.T) {
	t.Parallel()
	d := &ChannelDirectory{}
	if err := d.Put(&ChannelEntry{Platform: "telegram", Account: "bot", ChatID: "42", Label: "ops"}); err != nil {
		t.Fatal(err)
	}
	if err := d.Put(&ChannelEntry{Platform: "discord", Account: "app", ChatID: "7"}); err != nil {
		t.Fatal(err)
	}
	rows := d.Rows([]string{"telegram", "slack"})
	if len(rows) != 2 {
		t.Fatalf("rows = %v", rows)
	}
	if rows[0].Platform != "slack" || rows[0].ChatID != "" {
		t.Fatalf("missing entry row = %+v, want an empty row", rows[0])
	}
	if rows[1].Platform != "telegram" || rows[1].Label != "ops" {
		t.Fatalf("telegram row = %+v", rows[1])
	}
	if got := d.Get("nothing"); len(got) != 0 {
		t.Fatalf("unknown platform = %v", got)
	}
}

func TestF7GW025ManagedBot(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	off := &ManagedBot{}
	if err := off.ProvisionBot(t.Context()); !errors.Is(err, ErrModuleUnavailable) {
		t.Fatalf("disabled = %v, want ErrModuleUnavailable", err)
	}
	enabledNoCred := &ManagedBot{
		Enabled: true,
		Credential: func(context.Context) (string, bool, error) {
			return "", false, nil
		},
		Provision: func(context.Context, string) error {
			calls.Add(1)
			return nil
		},
	}
	if err := enabledNoCred.ProvisionBot(t.Context()); !errors.Is(err, ErrModuleUnavailable) {
		t.Fatalf("no credential = %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("network calls without credential = %d", calls.Load())
	}
	ready := &ManagedBot{
		Enabled: true,
		Credential: func(context.Context) (string, bool, error) {
			return "token", true, nil
		},
		Provision: func(context.Context, string) error {
			calls.Add(1)
			return nil
		},
	}
	if err := ready.ProvisionBot(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("provision calls = %d, want 1", calls.Load())
	}
}

type handoffAdapter struct{ calls int }

func (a *handoffAdapter) SendHandoff(_ context.Context, _, _, text string) (string, error) {
	a.calls++
	if !strings.Contains(text, "user: hello") {
		return "", errors.New("summary is not compact")
	}
	return "msg-1", nil
}

type handoffStore struct{ records []HandoffRecord }

func (s *handoffStore) RecordHandoff(_ context.Context, rec *HandoffRecord) error {
	s.records = append(s.records, *rec)
	return nil
}

func TestF7GW026Handoff(t *testing.T) {
	t.Parallel()
	adapter := &handoffAdapter{}
	store := &handoffStore{}
	h := &Handoff{Adapters: map[string]HandoffAdapter{"telegram": adapter}, Store: store}
	req := &HandoffRequest{
		SessionID: "s1", Platform: "Telegram", ChatID: "c1",
		Transcript: []TranscriptLine{{Role: "user", Text: "hello"}, {Role: "assistant", Text: "hi"}},
	}
	first, err := h.Execute(t.Context(), req)
	if err != nil || !first.Sent || first.MessageID != "msg-1" {
		t.Fatalf("first handoff = %+v, %v", first, err)
	}
	second, err := h.Execute(t.Context(), req)
	if err != nil || !second.Repeat || second.MessageID != first.MessageID {
		t.Fatalf("retry handoff = %+v, %v", second, err)
	}
	if adapter.calls != 1 {
		t.Fatalf("adapter calls = %d, want 1 (idempotent retry)", adapter.calls)
	}
	if len(store.records) != 1 || store.records[0].MessageID != "msg-1" {
		t.Fatalf("records = %+v", store.records)
	}
	missing := &Handoff{Adapters: map[string]HandoffAdapter{}}
	result, err := missing.Execute(t.Context(), &HandoffRequest{SessionID: "s1", Platform: "slack", ChatID: "c"})
	if err != nil || result.Sent || !strings.Contains(result.Message, "not active") {
		t.Fatalf("missing adapter = %+v, %v", result, err)
	}
	if _, err := h.Execute(t.Context(), &HandoffRequest{SessionID: "s1", Platform: "bad platform"}); !errors.Is(err, ErrInvalidPlatform) {
		t.Fatalf("invalid platform = %v", err)
	}
}

func TestF7SLASH012Handoff(t *testing.T) {
	t.Parallel()
	store := &handoffStore{}
	h := &Handoff{
		Adapters: map[string]HandoffAdapter{}, Store: store,
		Transcript: func(context.Context, string) ([]TranscriptLine, error) {
			return []TranscriptLine{{Role: "user", Text: "hello"}}, nil
		},
	}
	res, handled := h.Slash(t.Context(), "s1", "c1", "/handoff telegram")
	if !handled || !res.Handled || res.Applied {
		t.Fatalf("slash result = %+v handled=%v", res, handled)
	}
	if !strings.Contains(res.Reply, "not active") {
		t.Fatalf("reply = %q", res.Reply)
	}
	if len(store.records) != 0 {
		t.Fatalf("inactive adapter recorded %d handoffs", len(store.records))
	}
	usageRes, usageHandled := h.Slash(t.Context(), "s1", "c1", "/handoff")
	if !usageHandled || !strings.Contains(usageRes.Reply, "usage") {
		t.Fatalf("missing arg = %+v %v", usageRes, usageHandled)
	}
	if _, plainHandled := h.Slash(t.Context(), "s1", "c1", "plain text"); plainHandled {
		t.Fatal("plain text was treated as /handoff")
	}
	h.Adapters["telegram"] = &handoffAdapter{}
	activeRes, activeHandled := h.Slash(t.Context(), "s1", "c1", "/handoff telegram")
	if !activeHandled || !strings.Contains(activeRes.Reply, "msg-1") {
		t.Fatalf("active slash = %+v %v", activeRes, activeHandled)
	}
}

type approvalTransport struct {
	interactive bool
	sends       int
	fail        bool
}

func (t *approvalTransport) Send(context.Context, *ApprovalRequest) error {
	t.sends++
	if t.fail {
		return errors.New("transport down")
	}
	return nil
}

func (t *approvalTransport) Interactive() bool { return t.interactive }

func TestF7EXEC016ApprovalTransport(t *testing.T) {
	t.Parallel()
	builtin := &approvalTransport{interactive: true}
	external := &approvalTransport{interactive: true}
	router := &ApprovalRouter{Builtin: builtin, External: external}
	if err := router.Send(t.Context(), &ApprovalRequest{ID: "a", Class: "shell"}); err != nil {
		t.Fatal(err)
	}
	if external.sends != 1 || builtin.sends != 0 {
		t.Fatalf("external=%d builtin=%d, want external preferred", external.sends, builtin.sends)
	}
	router.External = nil
	if err := router.Send(t.Context(), &ApprovalRequest{ID: "b", Class: "shell"}); err != nil {
		t.Fatal(err)
	}
	if builtin.sends != 1 {
		t.Fatalf("builtin sends = %d, want 1", builtin.sends)
	}
}

func TestF7ApprovalTransportFailClosed(t *testing.T) {
	t.Parallel()
	failing := &approvalTransport{interactive: true, fail: true}
	router := &ApprovalRouter{Builtin: failing}
	if err := router.Send(t.Context(), &ApprovalRequest{ID: "a", Class: "shell"}); !errors.Is(err, ErrApprovalDenied) {
		t.Fatalf("failed transport = %v, want ErrApprovalDenied", err)
	}
	nonInteractive := &approvalTransport{interactive: false}
	router = &ApprovalRouter{External: nonInteractive}
	if err := router.Send(t.Context(), &ApprovalRequest{ID: "b", Class: ApprovalClassP26}); !errors.Is(err, ErrApprovalDenied) || !errors.Is(err, ErrApprovalInteractive) {
		t.Fatalf("P26 non-interactive = %v", err)
	}
	if nonInteractive.sends != 0 {
		t.Fatalf("non-interactive transport received %d P26 prompts", nonInteractive.sends)
	}
	if err := (&ApprovalRouter{}).Send(t.Context(), &ApprovalRequest{ID: "c"}); !errors.Is(err, ErrApprovalDenied) {
		t.Fatalf("no transport = %v", err)
	}
}
