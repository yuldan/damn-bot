package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// originDispatcher builds a dispatcher with an explicit approval origin.
func originDispatcher(t *testing.T, w *workspace, origin string, mode Mode, appr Approver, extra ...Tool) *Dispatcher {
	t.Helper()
	j := &journalRec{}
	reg, err := NewRegistry(append(append([]Tool{}, w.tools...), extra...)...)
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewDispatcher(&Config{
		Registry: reg, Journal: j, Spill: newMemSpill(), Redactor: testRedactor(),
		Mode: mode, Approver: appr, WorkspaceTrusted: true, Cwd: w.root, Origin: origin,
	})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// TestApprovalOrigin: the origin is a closed set, an empty config takes the
// headless `run` default, and every request carries the configured surface
// (ADR-197).
func TestApprovalOrigin(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		config, want string
	}{
		{"", OriginRun},
		{OriginRun, OriginRun},
		{OriginTUI, OriginTUI},
		{OriginACP, OriginACP},
		{OriginPlan, OriginPlan},
	} {
		t.Run("config="+c.config, func(t *testing.T) {
			w := newWorkspace(t)
			appr := &approverRec{answer: ApprovalAllowOnce}
			d := originDispatcher(t, w, c.config, ModeAsk, appr)
			out := d.Dispatch(context.Background(), callOf("write_file", `{"path":"a.txt","content":"x"}`))
			if out.Err != nil {
				t.Fatalf("write: %v", out.Err)
			}
			reqs := appr.requests()
			if len(reqs) != 1 || reqs[0].Origin != c.want {
				t.Fatalf("requests = %+v, want origin %q", reqs, c.want)
			}
		})
	}
	for _, bad := range []string{"headless", "web", "gateway", "mcp_serve", "TUI", " "} {
		if ValidOrigin(bad) {
			t.Errorf("ValidOrigin(%q) = true", bad)
		}
		reg, rerr := NewRegistry()
		if rerr != nil {
			t.Fatal(rerr)
		}
		if _, err := NewDispatcher(&Config{
			Registry: reg, Journal: &journalRec{}, Spill: newMemSpill(),
			Redactor: testRedactor(), Origin: bad,
		}); err == nil {
			t.Errorf("NewDispatcher accepted origin %q", bad)
		}
	}
}

// TestPreviewRequired: "dangerous" is derived from the classifier's closed
// labels plus workspace writes, never from a tool name (ADR-197).
func TestPreviewRequired(t *testing.T) {
	t.Parallel()
	cases := []struct {
		effect Effect
		class  string
		want   bool
	}{
		{EffectRead, "read", false},
		{EffectRead, "shell:destructive", false}, // effect read wins: nothing runs
		{EffectIdempotent, "write", true},
		{EffectNonIdempotent, "write", true},
		{EffectNonIdempotent, "shell:mutating", false},
		{EffectNonIdempotent, "shell:unknown", false},
		{EffectNonIdempotent, "shell:destructive", true},
		{EffectNonIdempotent, "shell:destructive flags:network", true},
		{EffectNonIdempotent, "shell:irreversible", true},
		{EffectNonIdempotent, "shell:irreversible flags:privileged", true},
		{EffectNonIdempotent, "shell:monetary", true},
		{EffectNonIdempotent, "shell:delegated_agent_bypass", true},
	}
	for _, c := range cases {
		if got := PreviewRequired(c.effect, c.class); got != c.want {
			t.Errorf("PreviewRequired(%s, %q) = %v, want %v", c.effect, c.class, got, c.want)
		}
	}
}

// TestApprovalRequestValidate: a dangerous action without a preview is never
// a valid request; an empty preview on a safe action is harmless.
func TestApprovalRequestValidate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		req     ApprovalRequest
		wantErr bool
	}{
		{name: "empty preview safe", req: ApprovalRequest{Tool: "read_file", Effect: EffectRead, Class: "read", Origin: OriginRun}},
		{name: "empty preview mutating shell", req: ApprovalRequest{Tool: "shell", Effect: EffectNonIdempotent, Class: "shell:mutating", Origin: OriginRun}},
		{name: "dangerous no preview", req: ApprovalRequest{Tool: "shell", Effect: EffectNonIdempotent, Class: "shell:destructive", Origin: OriginRun}, wantErr: true},
		{name: "write no preview", req: ApprovalRequest{Tool: "write_file", Effect: EffectIdempotent, Class: "write", Origin: OriginTUI}, wantErr: true},
		{name: "dangerous with preview", req: ApprovalRequest{Tool: "shell", Effect: EffectNonIdempotent, Class: "shell:destructive", Preview: "rm -rf x", Origin: OriginPlan}},
		{name: "whitespace preview", req: ApprovalRequest{Tool: "shell", Effect: EffectNonIdempotent, Class: "shell:irreversible", Preview: " \n\t", Origin: OriginACP}, wantErr: true},
		{name: "unknown origin", req: ApprovalRequest{Tool: "read_file", Effect: EffectRead, Class: "read", Origin: "web"}, wantErr: true},
		{name: "missing origin", req: ApprovalRequest{Tool: "read_file", Effect: EffectRead, Class: "read"}, wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.req.Validate()
			if (err != nil) != c.wantErr {
				t.Fatalf("Validate() = %v, wantErr %v", err, c.wantErr)
			}
		})
	}
}

// TestDangerousPreviewFilled: a dangerous decision from any Inspector reaches
// the approver with a non-empty preview, and the request passes Validate.
func TestDangerousPreviewFilled(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	st := &stubTool{
		name: "mutator", schema: echoSchema("mutator"), kind: KindEdit, effect: EffectIdempotent,
		in: func(context.Context, Env, json.RawMessage, PolicyContext) (PolicyDecision, error) {
			return PolicyDecision{Requirement: RequireAsk, Class: "write", AllowSession: true}, nil
		},
	}
	appr := &approverRec{answer: ApprovalAllowOnce}
	d := originDispatcher(t, w, OriginTUI, ModeAsk, appr, st)
	out := d.Dispatch(context.Background(), Call{Name: "mutator", CallID: "c1", Args: json.RawMessage(`{"body":"a.txt"}`)})
	if out.Err != nil {
		t.Fatalf("dispatch: %v", out.Err)
	}
	reqs := appr.requests()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d", len(reqs))
	}
	req := reqs[0]
	if err := req.Validate(); err != nil {
		t.Fatalf("request violates the contract: %v", err)
	}
	if !strings.Contains(req.Preview, "mutator") || !strings.Contains(req.Preview, "a.txt") {
		t.Fatalf("preview = %q", req.Preview)
	}
	if req.Origin != OriginTUI {
		t.Fatalf("origin = %q", req.Origin)
	}
}

// TestSafeEmptyPreviewRuns: a non-dangerous action with an empty preview is
// still approvable and runs (the contract only constrains dangerous calls).
func TestSafeEmptyPreviewRuns(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	st := &stubTool{
		name: "peek", schema: echoSchema("peek"), kind: KindOther, effect: EffectRead,
		in: func(context.Context, Env, json.RawMessage, PolicyContext) (PolicyDecision, error) {
			return PolicyDecision{Requirement: RequireAsk, Class: "read"}, nil
		},
	}
	appr := &approverRec{answer: ApprovalAllowOnce}
	d := originDispatcher(t, w, OriginACP, ModeAsk, appr, st)
	out := d.Dispatch(context.Background(), Call{Name: "peek", CallID: "c1", Args: json.RawMessage(`{"body":"x"}`)})
	if out.Err != nil {
		t.Fatalf("dispatch: %v", out.Err)
	}
	reqs := appr.requests()
	if len(reqs) != 1 || reqs[0].Preview != "" || reqs[0].Origin != OriginACP {
		t.Fatalf("request = %+v", reqs)
	}
	if st.CallCount() != 1 {
		t.Fatalf("tool ran %d times", st.CallCount())
	}
}
