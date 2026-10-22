package loop

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/loop/event"
)

// ---- fake provider -------------------------------------------------------

type streamFunc func(ctx context.Context, req *Request, emit func(Delta)) (Reply, error)

type fakeProvider struct {
	mu       sync.Mutex
	scripts  []streamFunc
	calls    int
	requests []Request
}

func (p *fakeProvider) Stream(ctx context.Context, req *Request, emit func(Delta)) (Reply, error) {
	p.mu.Lock()
	i := p.calls
	p.calls++
	p.requests = append(p.requests, *req)
	p.mu.Unlock()
	if len(p.scripts) == 0 {
		return Reply{}, &StreamError{Class: ClassProvider, Err: errors.New("no script")}
	}
	if i >= len(p.scripts) {
		i = len(p.scripts) - 1 // repeat the last script
	}
	return p.scripts[i](ctx, req, emit)
}

func textScript(text string, u event.Usage) streamFunc {
	return func(_ context.Context, _ *Request, emit func(Delta)) (Reply, error) {
		emit(Delta{Text: text})
		return Reply{Content: text, FinishReason: FinishStop, Usage: u, Model: "fake/m"}, nil
	}
}

// callScript emits each call in one delta, so the reply carries a full call.
func callScript(finish string, calls ...ToolCall) streamFunc {
	return func(_ context.Context, _ *Request, emit func(Delta)) (Reply, error) {
		var ds []ToolCallDelta
		for i, c := range calls {
			ds = append(ds, ToolCallDelta{Index: i, HasIndex: true, ID: c.ID, Name: c.Name, Arguments: c.Arguments})
		}
		emit(Delta{ToolCalls: ds})
		return Reply{FinishReason: finish, Model: "fake/m", ToolCalls: calls}, nil
	}
}

func errorScript(class Class, err error) streamFunc {
	return func(_ context.Context, _ *Request, _ func(Delta)) (Reply, error) {
		return Reply{}, &StreamError{Class: class, Err: err}
	}
}

// ---- fake assembler ------------------------------------------------------

type fakeAssembler struct {
	mu        sync.Mutex
	prompt    Prompt
	err       error
	calls     int
	histories [][]Message
}

func (a *fakeAssembler) Assemble(_ context.Context, req *AssembleRequest) (Prompt, error) {
	a.mu.Lock()
	a.calls++
	a.histories = append(a.histories, append([]Message(nil), req.History...))
	err := a.err
	p := a.prompt
	a.mu.Unlock()
	if err != nil {
		return Prompt{}, err
	}
	if p.Model == "" {
		p.Model = "fake/m"
	}
	if p.MaxTokens <= 0 {
		p.MaxTokens = 128
	}
	if p.WindowTokens == 0 {
		p.WindowTokens = 100000
	}
	return p, nil
}

// ---- fake toolset --------------------------------------------------------

type toolStub struct {
	prepare  func(ctx context.Context, c ToolCall) (Prepared, error)
	dispatch func(ctx context.Context, p *Prepared) (ToolResult, error)
}

type fakeToolset struct {
	mu       sync.Mutex
	stubs    map[string]*toolStub
	prepared []ToolCall
	ran      []Prepared
}

func newFakeToolset() *fakeToolset { return &fakeToolset{stubs: map[string]*toolStub{}} }

func (f *fakeToolset) add(name string, s *toolStub) { f.stubs[name] = s }

func (f *fakeToolset) Prepare(ctx context.Context, c ToolCall) (Prepared, error) {
	f.mu.Lock()
	f.prepared = append(f.prepared, c)
	s := f.stubs[c.Name]
	f.mu.Unlock()
	if s == nil {
		return Prepared{}, &ToolError{Code: "unknown_tool", Message: "no such tool: " + c.Name}
	}
	if s.prepare != nil {
		return s.prepare(ctx, c)
	}
	return Prepared{Call: c, Effect: EffectRead, Approval: ApprovalNone, Subject: "s"}, nil
}

func (f *fakeToolset) Dispatch(ctx context.Context, p *Prepared) (ToolResult, error) {
	f.mu.Lock()
	f.ran = append(f.ran, *p)
	s := f.stubs[p.Call.Name]
	f.mu.Unlock()
	if s != nil && s.dispatch != nil {
		return s.dispatch(ctx, p)
	}
	return ToolResult{Content: "tool-ok"}, nil
}

func (f *fakeToolset) ranCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.ran)
}

// ---- other fakes ---------------------------------------------------------

type fakeStore struct {
	mu     sync.Mutex
	events []event.Event
	turns  []string
	next   int64
	err    error
}

func (s *fakeStore) Append(_ context.Context, _, tid string, ev *event.Event) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return 0, s.err
	}
	s.next++
	s.events = append(s.events, *ev)
	s.turns = append(s.turns, tid)
	return s.next, nil
}

// turnOf returns the turn id of the first event of kind k.
func (s *fakeStore) turnOf(k event.Kind) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, e := range s.events {
		if e.Kind == k {
			return s.turns[i], true
		}
	}
	return "", false
}

func (s *fakeStore) kinds() []event.Kind {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]event.Kind, 0, len(s.events))
	for _, e := range s.events {
		out = append(out, e.Kind)
	}
	return out
}

func (s *fakeStore) find(k event.Kind) (event.Event, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.events {
		if e.Kind == k {
			return e, true
		}
	}
	return event.Event{}, false
}

type fakeLedger struct {
	mu       sync.Mutex
	usage    []event.Usage
	eventIDs []int64
	err      error
}

func (l *fakeLedger) Record(_ context.Context, _, _ string, eventID int64, u *event.Usage) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil {
		return l.err
	}
	l.usage = append(l.usage, *u)
	l.eventIDs = append(l.eventIDs, eventID)
	return nil
}

type fakeApprover struct {
	mu      sync.Mutex
	answers []ApprovalResult
	reqs    []ApprovalRequest
	err     error
}

func (a *fakeApprover) Approve(_ context.Context, req *ApprovalRequest) (ApprovalResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reqs = append(a.reqs, *req)
	if a.err != nil {
		return ApprovalResult{}, a.err
	}
	if len(a.answers) == 0 {
		return ApprovalResult{Decision: DecisionDeny}, nil
	}
	r := a.answers[0]
	if len(a.answers) > 1 {
		a.answers = a.answers[1:]
	}
	return r, nil
}

type fakeSpiller struct {
	mu    sync.Mutex
	ref   string
	err   error
	calls int
}

func (s *fakeSpiller) Spill(_ context.Context, _ string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.ref, s.err
}

type fakeRedactor struct{}

func (fakeRedactor) Redact(s string) string { return strings.ReplaceAll(s, "sk-secret", "[REDACTED]") }

// ---- fixture -------------------------------------------------------------

type fixture struct {
	cfg      Config
	provider *fakeProvider
	asm      *fakeAssembler
	tools    *fakeToolset
	store    *fakeStore
	ledger   *fakeLedger
	carriers *fakeCarrierStore
	approver *fakeApprover
	spiller  *fakeSpiller
	redactor Redactor
	validate func(string) error
	stream   func() StreamFilter

	cancel  context.CancelFunc
	loopRef *Loop
}

func newFixture() *fixture {
	return &fixture{
		cfg: Config{
			Now:       func() time.Time { return time.Unix(1_700_000_000, 0) },
			NewTurnID: func() string { return "T1" },
		},
		provider: &fakeProvider{scripts: []streamFunc{textScript("ok", event.Usage{Input: 3, Output: 1})}},
		asm:      &fakeAssembler{},
		tools:    newFakeToolset(),
		store:    &fakeStore{},
		ledger:   &fakeLedger{},
	}
}

func (f *fixture) deps() Deps {
	return Deps{
		Provider: f.provider, Assembler: f.asm, Toolset: f.tools, Store: f.store,
		Ledger: f.ledger, Approver: f.approver, Spiller: f.spiller, Redactor: f.redactor,
		Carriers: f.carriers, NewRedactStream: f.stream, ValidateAnswer: f.validate,
	}
}

func (f *fixture) run(t *testing.T) []event.Update {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.cancel = cancel
	deps := f.deps()
	l, err := New(&f.cfg, &deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	f.loopRef = l
	out, err := l.Prompt(ctx, "S1", Input{Text: "hello"})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	var ups []event.Update
	for u := range out {
		ups = append(ups, u)
	}
	if l.Active("S1") {
		t.Error("turn still active after the stream closed")
	}
	return ups
}

// terminal returns the single stop update.
func terminal(t *testing.T, ups []event.Update) event.Update {
	t.Helper()
	var stops []event.Update
	for i := range ups {
		if ups[i].Kind == event.UpdateStop {
			stops = append(stops, ups[i])
		}
	}
	if len(stops) != 1 {
		t.Fatalf("got %d stop updates, want 1: %+v", len(stops), ups)
	}
	return stops[0]
}

// payloadOf decodes an event payload.
func payloadOf(t *testing.T, e *event.Event, v any) {
	t.Helper()
	if err := json.Unmarshal(e.Payload, v); err != nil {
		t.Fatalf("payload %s: %v", e.Payload, err)
	}
}

// countKind counts the stored events of one kind.
func (s *fakeStore) countKind(k event.Kind) int {
	n := 0
	for _, got := range s.kinds() {
		if got == k {
			n++
		}
	}
	return n
}
