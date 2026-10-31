package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/damnbot/damnbot/internal/modules/host"
)

type testHost struct {
	completer host.Completer
}

var _ host.Host = (*testHost)(nil)

func (h *testHost) Emit(context.Context, host.Event) error { return nil }
func (h *testHost) I18n() host.Translator                  { return nil }
func (h *testHost) Spawn(context.Context, host.SpawnSpec) (host.Process, error) {
	return nil, errors.New("unused")
}
func (h *testHost) LLM(string) host.Completer { return h.completer }
func (h *testHost) Storage(string) (host.Store, error) {
	return nil, errors.New("unused")
}

// seamHost opts into the capability seam; testHost alone must stay fail-closed.
type seamHost struct {
	*testHost
	caps host.Capabilities
}

var _ host.CapabilityProvider = seamHost{}

func (h seamHost) Capabilities() host.Capabilities { return h.caps }

type fakeBudget struct {
	err   error
	calls int
	req   host.BudgetRequest
}

func (b *fakeBudget) Check(_ context.Context, req host.BudgetRequest) error {
	b.calls++
	b.req = req
	return b.err
}

type fakeCompleter struct {
	resp  host.Response
	err   error
	calls int
}

func (c *fakeCompleter) Complete(context.Context, host.Request) (host.Response, error) {
	c.calls++
	return c.resp, c.err
}

func samplingHost(budget host.Budget, completer host.Completer) seamHost {
	return seamHost{testHost: &testHost{completer: completer}, caps: host.Capabilities{Budget: budget}}
}

func samplingRequest() host.Request {
	return host.Request{
		Model:           "light",
		Messages:        []host.Message{{Role: "user", Content: "hi"}},
		MaxOutputTokens: 64,
	}
}

func TestSamplingRequiresOptIn(t *testing.T) {
	t.Parallel()
	budget, completer := &fakeBudget{}, &fakeCompleter{}
	s := NewSampling(samplingHost(budget, completer), false)
	if _, err := s.Complete(context.Background(), samplingRequest()); !errors.Is(err, ErrSamplingDisabled) {
		t.Fatalf("Complete = %v, want ErrSamplingDisabled", err)
	}
	if budget.calls != 0 || completer.calls != 0 {
		t.Fatalf("disabled sampling touched budget=%d completer=%d", budget.calls, completer.calls)
	}
}

func TestSamplingFailsClosedWithoutCapabilities(t *testing.T) {
	t.Parallel()
	completer := &fakeCompleter{}
	s := NewSampling(&testHost{completer: completer}, true)
	if _, err := s.Complete(context.Background(), samplingRequest()); !errors.Is(err, host.ErrBudgetDenied) {
		t.Fatalf("Complete = %v, want ErrBudgetDenied from the default guard", err)
	}
	if completer.calls != 0 {
		t.Fatalf("unwired budget still reached the completer %d times", completer.calls)
	}
}

func TestSamplingBudgetDenied(t *testing.T) {
	t.Parallel()
	budget := &fakeBudget{err: host.ErrBudgetDenied}
	completer := &fakeCompleter{}
	s := NewSampling(samplingHost(budget, completer), true)
	if _, err := s.Complete(context.Background(), samplingRequest()); !errors.Is(err, host.ErrBudgetDenied) {
		t.Fatalf("Complete = %v, want ErrBudgetDenied", err)
	}
	if budget.calls != 1 || completer.calls != 0 {
		t.Fatalf("budget calls = %d, completer calls = %d", budget.calls, completer.calls)
	}
	want := host.BudgetRequest{Purpose: "delegate", Model: "light", MaxOutputTokens: 64}
	if budget.req != want {
		t.Fatalf("budget request = %+v, want %+v", budget.req, want)
	}
}

func TestSamplingBudgetAllowed(t *testing.T) {
	t.Parallel()
	budget := &fakeBudget{}
	completer := &fakeCompleter{resp: host.Response{Content: "sampled"}}
	s := NewSampling(samplingHost(budget, completer), true)
	resp, err := s.Complete(context.Background(), samplingRequest())
	if err != nil || resp.Content != "sampled" {
		t.Fatalf("Complete = %+v, %v", resp, err)
	}
	if budget.calls != 1 || completer.calls != 1 {
		t.Fatalf("budget calls = %d, completer calls = %d", budget.calls, completer.calls)
	}
}

func TestSamplingValidation(t *testing.T) {
	t.Parallel()
	budget, completer := &fakeBudget{}, &fakeCompleter{}
	s := NewSampling(samplingHost(budget, completer), true)
	req := samplingRequest()
	req.MaxOutputTokens = 0
	if _, err := s.Complete(context.Background(), req); !errors.Is(err, ErrSamplingInvalid) {
		t.Fatalf("Complete = %v, want ErrSamplingInvalid", err)
	}
	if budget.calls != 0 {
		t.Fatal("invalid request reached the budget guard")
	}

	unavailable := NewSampling(samplingHost(&fakeBudget{}, nil), true)
	if _, err := unavailable.Complete(context.Background(), samplingRequest()); !errors.Is(err, ErrSamplingUnavailable) {
		t.Fatalf("nil completer = %v, want ErrSamplingUnavailable", err)
	}
}

func TestSamplingWrapsCompleterError(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	s := NewSampling(samplingHost(&fakeBudget{}, &fakeCompleter{err: boom}), true)
	if _, err := s.Complete(context.Background(), samplingRequest()); !errors.Is(err, boom) {
		t.Fatalf("Complete = %v, want wrapped boom", err)
	}
}

func TestToolEffectDefaultsToNonIdempotent(t *testing.T) {
	t.Parallel()
	cases := map[string]host.Effect{
		"":               host.EffectNonIdempotent,
		"  ":             host.EffectNonIdempotent,
		"bogus":          host.EffectNonIdempotent,
		"read":           host.EffectRead,
		"READ":           host.EffectRead,
		"read_only":      host.EffectNonIdempotent, // unknown value: fail closed
		" idempotent ":   host.EffectIdempotent,
		"non_idempotent": host.EffectNonIdempotent,
		"destructive":    host.EffectDestructive,
		"irreversible":   host.EffectIrreversible,
	}
	for annotation, want := range cases {
		if got := ToolEffect(annotation); got != want {
			t.Errorf("ToolEffect(%q) = %q, want %q", annotation, got, want)
		}
	}
}

type approverFunc func(context.Context, *host.ApprovalRequest) (host.Approval, error)

func (f approverFunc) Approve(ctx context.Context, req *host.ApprovalRequest) (host.Approval, error) {
	return f(ctx, req)
}

func TestApproveFailsClosed(t *testing.T) {
	t.Parallel()
	req := host.ApprovalRequest{Tool: "mcp__srv__write", Effect: host.EffectNonIdempotent, Preview: "write x"}
	answer, err := Approve(context.Background(), &testHost{}, &req)
	if err != nil || answer != host.ApprovalDeny {
		t.Fatalf("Approve = %v, %v; want deny, nil", answer, err)
	}

	read := req
	read.Effect = host.EffectRead
	answer, err = Approve(context.Background(), &testHost{}, &read)
	if err != nil || answer != host.ApprovalAllowOnce {
		t.Fatalf("read Approve = %v, %v; want allow once, nil", answer, err)
	}

	invalid := req
	invalid.Preview = ""
	if _, err := Approve(context.Background(), &testHost{}, &invalid); !errors.Is(err, host.ErrInvalidApproval) {
		t.Fatalf("invalid Approve = %v, want ErrInvalidApproval", err)
	}

	if _, err := Approve(context.Background(), &testHost{}, nil); !errors.Is(err, host.ErrInvalidApproval) {
		t.Fatalf("nil Approve = %v, want ErrInvalidApproval", err)
	}
}

func TestApproveUsesWiredChannel(t *testing.T) {
	t.Parallel()
	calls := 0
	h := seamHost{testHost: &testHost{}, caps: host.Capabilities{
		Approvals: approverFunc(func(context.Context, *host.ApprovalRequest) (host.Approval, error) {
			calls++
			return host.ApprovalAllowSession, nil
		}),
	}}
	req := host.ApprovalRequest{Tool: "mcp__srv__write", Effect: host.EffectIdempotent, Preview: "write x"}
	answer, err := Approve(context.Background(), h, &req)
	if err != nil || answer != host.ApprovalAllowSession || calls != 1 {
		t.Fatalf("Approve = %v, %v, calls=%d", answer, err, calls)
	}

	boom := errors.New("boom")
	broken := seamHost{testHost: &testHost{}, caps: host.Capabilities{
		Approvals: approverFunc(func(context.Context, *host.ApprovalRequest) (host.Approval, error) {
			return host.ApprovalAllowOnce, boom
		}),
	}}
	if _, err := Approve(context.Background(), broken, &req); !errors.Is(err, boom) {
		t.Fatalf("Approve error = %v, want wrapped boom", err)
	}
}
