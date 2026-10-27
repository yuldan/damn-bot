package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/loop"
)

func TestModeParse(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		in   string
		want Mode
	}{
		{"ask", ModeAsk}, {"auto-edit", ModeAutoEdit}, {"auto", ModeAuto}, {"plan", ModePlan},
	} {
		got, err := ParseMode(c.in)
		if err != nil || got != c.want {
			t.Errorf("ParseMode(%q) = %s, %v", c.in, got, err)
		}
	}
	for _, bad := range []string{"", "PLAN", "readonly", "ask "} {
		if _, err := ParseMode(bad); !errors.Is(err, ErrMode) {
			t.Errorf("ParseMode(%q) = %v", bad, err)
		}
	}
	if !ModePlan.Plan() || ModeAsk.Plan() {
		t.Fatal("Plan() wrong")
	}
}

func TestSessionModeAndPromptInput(t *testing.T) {
	t.Parallel()
	s, err := NewSession("s", 2)
	if err != nil {
		t.Fatal(err)
	}
	if s.Mode() != ModeAsk {
		t.Fatalf("default mode = %s", s.Mode())
	}
	if err := s.SetMode(ModePlan); err != nil {
		t.Fatal(err)
	}
	if !s.PlanMode() || !s.PromptInput("x").PlanMode || s.PromptInput("x").Text != "x" {
		t.Fatalf("plan prompt input = %+v", s.PromptInput("x"))
	}
	if err := s.SetMode(Mode(99)); !errors.Is(err, ErrMode) {
		t.Fatalf("invalid mode = %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMode(ModeAuto); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("closed set mode = %v", err)
	}
}

func TestPermissionValidate(t *testing.T) {
	t.Parallel()
	req := func(effect, class, origin, preview string) PermissionRequest {
		return PermissionRequest{Tool: "shell", Effect: effect, Class: class, Origin: origin, Preview: preview}
	}
	for _, c := range []struct {
		name string
		req  PermissionRequest
		want error
	}{
		{"safe read", req("read", "read", Origin, ""), nil},
		{"mutating shell safe", req("non_idempotent", "shell:mutating", Origin, ""), nil},
		{"destructive no preview", req("non_idempotent", "shell:destructive", Origin, ""), ErrPreviewRequired},
		{"irreversible no preview", req("non_idempotent", "shell:irreversible", Origin, " "), ErrPreviewRequired},
		{"write no preview", req("idempotent", "write", Origin, ""), ErrPreviewRequired},
		{"unknown non-read", req("non_idempotent", "", Origin, ""), ErrPreviewRequired},
		{"dangerous with preview", req("non_idempotent", "shell:destructive", Origin, "rm -rf x"), nil},
		{"wrong origin", req("read", "read", "tui", ""), ErrOrigin},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			err := c.req.Validate()
			if c.want == nil {
				if err != nil {
					t.Fatalf("Validate = %v, want nil", err)
				}
			} else if !errors.Is(err, c.want) {
				t.Fatalf("Validate = %v, want %v", err, c.want)
			}
		})
	}
	if err := (&PermissionRequest{Origin: Origin, Effect: "read"}).Validate(); !errors.Is(err, ErrPermission) {
		t.Fatalf("empty tool = %v", err)
	}
}

func TestClipPreview(t *testing.T) {
	t.Parallel()
	short := "command: rm -rf x"
	if ClipPreview(short) != short {
		t.Fatal("short preview changed")
	}
	long := strings.Repeat("a", MaxPreview+100)
	got := ClipPreview(long)
	if len([]rune(got)) > MaxPreview+len("\n… (preview truncated)") {
		t.Fatalf("clipped preview too long: %d", len([]rune(got)))
	}
	if !strings.Contains(got, "preview truncated") {
		t.Fatal("no truncation marker")
	}
}

func TestPermissionOptions(t *testing.T) {
	t.Parallel()
	write := &PermissionRequest{Class: "write", AllowSession: true}
	if got := write.Options(); len(got) != 3 || got[2].OptionID != "allow_always" {
		t.Fatalf("write options = %+v", got)
	}
	for _, class := range []string{"shell:destructive", "shell:irreversible", "shell:monetary", "shell:delegated_agent_bypass"} {
		r := &PermissionRequest{Class: class, AllowSession: true}
		for _, o := range r.Options() {
			if o.OptionID == "allow_always" {
				t.Fatalf("%s offered allow_always", class)
			}
		}
	}
	noSession := &PermissionRequest{Class: "write", AllowSession: false}
	if len(noSession.Options()) != 2 {
		t.Fatalf("noSession options = %+v", noSession.Options())
	}
}

func TestDecodePermissionResult(t *testing.T) {
	t.Parallel()
	cases := []struct {
		raw  string
		want PermissionAnswer
		err  bool
	}{
		{`{"outcome":{"outcome":"selected","optionId":"allow_once"}}`, PermissionAllowOnce, false},
		{`{"outcome":{"outcome":"selected","optionId":"allow_always"}}`, PermissionAllowAlways, false},
		{`{"outcome":{"outcome":"selected","optionId":"reject_once"}}`, PermissionReject, false},
		{`{"outcome":{"outcome":"cancelled"}}`, PermissionReject, false},
		{`{"outcome":{"outcome":"selected","optionId":"yolo"}}`, PermissionReject, true},
		{`{"outcome":{"outcome":"something"}}`, PermissionReject, true},
		{`{`, PermissionReject, true},
	}
	for _, c := range cases {
		got, err := DecodePermissionResult(json.RawMessage(c.raw))
		if got != c.want || (err != nil) != c.err {
			t.Errorf("%s = %v, %v (want %v, err %v)", c.raw, got, err, c.want, c.err)
		}
	}
}

type recordingPermitter struct {
	reqs   []PermissionRequest
	answer PermissionAnswer
	err    error
	block  bool
}

//nolint:gocritic // Permitter fixes the by-value request
func (p *recordingPermitter) Ask(ctx context.Context, req PermissionRequest) (PermissionAnswer, error) {
	p.reqs = append(p.reqs, req)
	if p.block {
		<-ctx.Done()
		return PermissionReject, fmt.Errorf("blocked: %w", ctx.Err())
	}
	return p.answer, p.err
}

func TestApproverMapsAnswers(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		answer PermissionAnswer
		want   loop.Decision
	}{
		{PermissionAllowOnce, loop.DecisionAllowOnce},
		{PermissionAllowAlways, loop.DecisionAllowOnce}, // no AllowSession in a loop request
		{PermissionReject, loop.DecisionDeny},
	} {
		perm := &recordingPermitter{answer: c.answer}
		reg := NewRegistry(2)
		if _, err := reg.CreateWith("s", Options{Permitter: perm}); err != nil {
			t.Fatal(err)
		}
		a := &Approver{Sessions: reg}
		res, err := a.Approve(context.Background(), &loop.ApprovalRequest{
			SessionID: "s", Tool: "shell", Effect: loop.EffectNonIdempotent,
			Preview: "command: rm -rf x\nclass: destructive", Origin: loop.OriginACP,
		})
		if err != nil || res.Decision != c.want {
			t.Fatalf("answer %s = %s, %v", c.answer, res.Decision, err)
		}
		if len(perm.reqs) != 1 {
			t.Fatalf("asks = %d", len(perm.reqs))
		}
		req := perm.reqs[0]
		if req.Origin != Origin || req.Preview == "" || req.Effect != "non_idempotent" {
			t.Fatalf("request = %+v", req)
		}
	}
}

//nolint:gocognit,gocyclo // one subtest per denial reason
func TestApproverDeniesWithoutAsking(t *testing.T) {
	t.Parallel()
	base := func() (*recordingPermitter, *Registry) {
		perm := &recordingPermitter{answer: PermissionAllowOnce}
		reg := NewRegistry(2)
		if _, err := reg.CreateWith("s", Options{Permitter: perm}); err != nil {
			t.Fatal(err)
		}
		return perm, reg
	}
	req := func() *loop.ApprovalRequest {
		return &loop.ApprovalRequest{
			SessionID: "s", Tool: "write_file", Effect: loop.EffectIdempotent,
			Preview: "write a", Origin: loop.OriginACP,
		}
	}
	t.Run("plan mode", func(t *testing.T) {
		perm, reg := base()
		s, _ := reg.Get("s")
		if err := s.SetMode(ModePlan); err != nil {
			t.Fatal(err)
		}
		res, err := (&Approver{Sessions: reg}).Approve(context.Background(), req())
		if err != nil || res.Decision != loop.DecisionDeny || len(perm.reqs) != 0 {
			t.Fatalf("plan = %s, %v, asks %d", res.Decision, err, len(perm.reqs))
		}
	})
	t.Run("wrong origin", func(t *testing.T) {
		perm, reg := base()
		r := req()
		r.Origin = loop.OriginTUI
		res, err := (&Approver{Sessions: reg}).Approve(context.Background(), r)
		if err != nil || res.Decision != loop.DecisionDeny || len(perm.reqs) != 0 {
			t.Fatalf("origin = %s, %v, asks %d", res.Decision, err, len(perm.reqs))
		}
	})
	t.Run("missing preview", func(t *testing.T) {
		perm, reg := base()
		r := req()
		r.Preview = ""
		res, err := (&Approver{Sessions: reg}).Approve(context.Background(), r)
		if err != nil || res.Decision != loop.DecisionDeny || len(perm.reqs) != 0 {
			t.Fatalf("preview = %s, %v, asks %d", res.Decision, err, len(perm.reqs))
		}
	})
	t.Run("no permitter", func(t *testing.T) {
		reg := NewRegistry(2)
		if _, err := reg.CreateWith("s", Options{}); err != nil {
			t.Fatal(err)
		}
		res, err := (&Approver{Sessions: reg}).Approve(context.Background(), req())
		if err != nil || res.Decision != loop.DecisionDeny {
			t.Fatalf("no permitter = %s, %v", res.Decision, err)
		}
	})
	t.Run("missing session", func(t *testing.T) {
		reg := NewRegistry(2)
		res, err := (&Approver{Sessions: reg}).Approve(context.Background(), req())
		if err != nil || res.Decision != loop.DecisionDeny {
			t.Fatalf("missing = %s, %v", res.Decision, err)
		}
	})
}

func TestApproverTimeoutIsReject(t *testing.T) {
	t.Parallel()
	perm := &recordingPermitter{block: true}
	reg := NewRegistry(2)
	if _, err := reg.CreateWith("s", Options{Permitter: perm}); err != nil {
		t.Fatal(err)
	}
	a := &Approver{Sessions: reg, Timeout: 20 * time.Millisecond}
	start := time.Now()
	res, err := a.Approve(context.Background(), &loop.ApprovalRequest{
		SessionID: "s", Tool: "shell", Effect: loop.EffectNonIdempotent,
		Preview: "command: ls\nclass: mutating", Origin: loop.OriginACP,
	})
	took := time.Since(start)
	if err != nil || res.Decision != loop.DecisionDeny {
		t.Fatalf("timeout = %s, %v", res.Decision, err)
	}
	if took < 10*time.Millisecond || took > time.Second {
		t.Fatalf("timeout took %v", took)
	}
}

func TestPermissionParams(t *testing.T) {
	t.Parallel()
	req := PermissionRequest{
		SessionID: "s", Tool: "write_file", ToolCallID: "c1", Class: "write",
		Effect: "idempotent", Preview: strings.Repeat("x", MaxPreview+10),
		Origin: Origin, Args: json.RawMessage(`{"path":"a"}`), AllowSession: true,
	}
	p := req.Params()
	if p.SessionID != "s" || p.ToolCall.ToolCallID != "c1" || len(p.Options) != 3 {
		t.Fatalf("params = %+v", p)
	}
	if len([]rune(p.Preview)) > MaxPreview+len("\n… (preview truncated)") {
		t.Fatal("params preview not clipped")
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "preview truncated") {
		t.Fatal("preview truncation marker missing")
	}
}
