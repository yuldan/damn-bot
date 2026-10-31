package host

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// capsHost adds the optional capability seam to the five-method test host.
type capsHost struct {
	*fakeHost
	caps Capabilities
}

var _ CapabilityProvider = capsHost{}

func (h capsHost) Capabilities() Capabilities { return h.caps }

func TestCapabilitiesFailClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	caps := CapabilitiesOf(&fakeHost{})

	answer, err := caps.Approvals.Approve(ctx, &ApprovalRequest{
		Tool: "mcp__server__write", CallID: "c1", Effect: EffectNonIdempotent, Preview: "write x",
	})
	if err != nil || answer != ApprovalDeny || answer.Allowed() {
		t.Fatalf("default Approve = %v, %v; want deny, nil", answer, err)
	}
	if err := caps.Budget.Check(ctx, BudgetRequest{Purpose: "delegate", MaxOutputTokens: 64}); !errors.Is(err, ErrBudgetDenied) {
		t.Fatalf("default Budget.Check = %v, want ErrBudgetDenied", err)
	}
	if _, err := caps.Secrets.ChildEnv(AllowPolicy{Inherit: []string{"PATH"}}); !errors.Is(err, ErrSecretsUnavailable) {
		t.Fatalf("default Secrets.ChildEnv = %v, want ErrSecretsUnavailable", err)
	}
	if _, err := caps.Clock.Now(); !errors.Is(err, ErrClockUnavailable) {
		t.Fatalf("default Clock.Now = %v, want ErrClockUnavailable", err)
	}
}

func TestCapabilitiesFillMissingFields(t *testing.T) {
	t.Parallel()
	calls := 0
	approver := approverFunc(func(context.Context, *ApprovalRequest) (Approval, error) {
		calls++
		return ApprovalAllowOnce, nil
	})
	caps := CapabilitiesOf(capsHost{fakeHost: &fakeHost{}, caps: Capabilities{Approvals: approver}})
	if _, err := caps.Approvals.Approve(context.Background(), &ApprovalRequest{}); err != nil || calls != 1 {
		t.Fatalf("wired Approve calls = %d, %v", calls, err)
	}
	if err := caps.Budget.Check(context.Background(), BudgetRequest{}); !errors.Is(err, ErrBudgetDenied) {
		t.Fatalf("missing Budget = %v, want ErrBudgetDenied", err)
	}
	if _, err := caps.Secrets.ChildEnv(AllowPolicy{}); !errors.Is(err, ErrSecretsUnavailable) {
		t.Fatalf("missing Secrets = %v, want ErrSecretsUnavailable", err)
	}
	if _, err := caps.Clock.Now(); !errors.Is(err, ErrClockUnavailable) {
		t.Fatalf("missing Clock = %v, want ErrClockUnavailable", err)
	}
}

type approverFunc func(context.Context, *ApprovalRequest) (Approval, error)

func (f approverFunc) Approve(ctx context.Context, req *ApprovalRequest) (Approval, error) {
	return f(ctx, req)
}

func TestApprovalRequestValidate(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", MaxApprovalPreview+1)
	cases := []struct {
		name string
		req  ApprovalRequest
		ok   bool
	}{
		{"read", ApprovalRequest{Tool: "mcp__srv__search", Effect: EffectRead}, true},
		{"non-idempotent", ApprovalRequest{Tool: "mcp__srv__write", Effect: EffectNonIdempotent, Preview: "write x", Args: json.RawMessage(`{"a":1}`)}, true},
		{"irreversible", ApprovalRequest{Tool: "mcp__srv__drop", Effect: EffectIrreversible, Preview: "drop table"}, true},
		{"no preview", ApprovalRequest{Tool: "mcp__srv__write", Effect: EffectNonIdempotent}, false},
		{"unknown effect", ApprovalRequest{Tool: "mcp__srv__write", Effect: Effect("auto"), Preview: "x"}, false},
		{"zero effect", ApprovalRequest{Tool: "mcp__srv__write", Preview: "x"}, false},
		{"no tool", ApprovalRequest{Effect: EffectRead}, false},
		{"bad args", ApprovalRequest{Tool: "mcp__srv__write", Effect: EffectNonIdempotent, Preview: "x", Args: json.RawMessage(`{`)}, false},
		{"long preview", ApprovalRequest{Tool: "mcp__srv__write", Effect: EffectNonIdempotent, Preview: long}, false},
		{"long tool", ApprovalRequest{Tool: strings.Repeat("t", MaxApprovalField+1), Effect: EffectRead}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.req.Validate()
			if c.ok && err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if !c.ok && !errors.Is(err, ErrInvalidApproval) {
				t.Fatalf("Validate = %v, want ErrInvalidApproval", err)
			}
		})
	}
}

func TestEffectNeedsApproval(t *testing.T) {
	t.Parallel()
	if EffectRead.NeedsApproval() || EffectRead.PreviewRequired() {
		t.Fatal("read effect asks for consent")
	}
	for _, e := range []Effect{EffectIdempotent, EffectNonIdempotent, EffectDestructive, EffectIrreversible, Effect("unknown")} {
		if !e.NeedsApproval() || !e.PreviewRequired() {
			t.Errorf("effect %q must ask with a preview", e)
		}
	}
}

func TestAllowPolicyEnv(t *testing.T) {
	t.Parallel()
	parent := []string{
		"PATH=/bin",
		"HOME=/home/u",
		"OPENAI_API_KEY=sk-secret",
		"LANG=en_US.UTF-8",
		"NOEQUALS",
	}
	got, err := AllowPolicy{
		Inherit: []string{"PATH", "HOME", "LANG", "MISSING"},
		Set:     map[string]string{"HOME": "/child", "MCP_MODE": "stdio"},
	}.Env(parent)
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	want := []string{"PATH=/bin", "LANG=en_US.UTF-8", "HOME=/child", "MCP_MODE=stdio"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Env = %q, want %q", got, want)
	}
	if env := strings.Join(got, "\x00"); strings.Contains(env, "sk-secret") || strings.Contains(env, "OPENAI_API_KEY") {
		t.Fatal("Env inherited a secret not named in the allowlist")
	}
}

func TestAllowPolicyEnvRejectsMalformed(t *testing.T) {
	t.Parallel()
	cases := []AllowPolicy{
		{Inherit: []string{""}},
		{Inherit: []string{"A=B"}},
		{Set: map[string]string{"": "x"}},
		{Set: map[string]string{"A=B": "x"}},
		{Set: map[string]string{"A": "x\x00y"}},
	}
	for _, p := range cases {
		if _, err := p.Env(nil); !errors.Is(err, ErrInvalidEnv) {
			t.Errorf("Env(%+v) = %v, want ErrInvalidEnv", p, err)
		}
	}
}
