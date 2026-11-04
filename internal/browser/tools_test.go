//go:build !minimal

package browser

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/modules/host"
)

func invoke(t *testing.T, m *Module, env host.Env, tool, args string) (host.Result, error) {
	t.Helper()
	return m.Invoke(context.Background(), tool, json.RawMessage(args), env)
}

func TestSnapshotTaint(t *testing.T) {
	t.Parallel()
	f := newFake()
	f.results["Accessibility.getFullAXTree"] = json.RawMessage(`{"nodes":[{"nodeId":1,"role":{"value":"button"},"name":{"value":"Save"},"backendDOMNodeId":7},{"nodeId":2,"role":{"value":"textbox"},"name":{"value":"Password"}}]}`)
	cfg := moduleConfig()
	m := newModule(t.Context(), t, &cfg, Deps{Dial: dialTo(f)})
	res, err := invoke(t, m, host.Env{}, "browser_snapshot", `{}`)
	if err != nil || res.UntrustedSource != "browser:page" {
		t.Fatalf("snapshot = %+v, %v", res, err)
	}
	if !strings.Contains(res.Content, `"ref":7`) || strings.Contains(res.Content, "Password") {
		t.Fatalf("snapshot content = %s", res.Content)
	}
}

func TestBackNeedsHistoryAndApproval(t *testing.T) {
	t.Parallel()
	appr := &fakeApprovals{answer: host.ApprovalAllowOnce}
	f := newFake()
	cfg := moduleConfig()
	m := newModule(t.Context(), t, &cfg, Deps{Dial: dialTo(f), Approvals: appr})
	_, err := invoke(t, m, host.Env{CallID: "c1"}, "browser_back", `{}`)
	var he *host.Error
	if !errors.As(err, &he) || he.Code != "invalid_args" {
		t.Fatalf("back without history = %v, want invalid_args", err)
	}
	f.results["Page.getNavigationHistory"] = json.RawMessage(`{"currentIndex":1,"entries":[{"id":4,"url":"https://app.example/a"},{"id":5,"url":"https://app.example/b"}]}`)
	res, err := invoke(t, m, host.Env{CallID: "c2"}, "browser_back", `{}`)
	if err != nil || !strings.Contains(res.Content, "https://app.example/a") {
		t.Fatalf("back = %+v, %v", res, err)
	}
	if !f.called("Page.navigateToHistoryEntry") || len(appr.requests()) != 2 {
		t.Fatalf("calls = %v, approvals = %d", f.calls, len(appr.requests()))
	}
}

func TestPointerActions(t *testing.T) {
	t.Parallel()
	appr := &fakeApprovals{answer: host.ApprovalAllowOnce}
	f := newFake()
	cfg := moduleConfig()
	m := newModule(t.Context(), t, &cfg, Deps{Dial: dialTo(f), Approvals: appr})
	env := host.Env{CallID: "c1"}
	for _, tc := range []struct{ tool, args string }{
		{"browser_hover", `{"x":1,"y":2}`},
		{"browser_drag", `{"x":1,"y":2,"end_x":3,"end_y":4}`},
		{"browser_scroll", `{"x":0,"y":0,"delta_x":1,"delta_y":2}`},
	} {
		if _, err := invoke(t, m, env, tc.tool, tc.args); err != nil {
			t.Fatalf("%s: %v", tc.tool, err)
		}
	}
	if !strings.Contains(strings.Join(f.calls, ","), "Input.dispatchMouseEvent") {
		t.Fatalf("calls = %v", f.calls)
	}
	_, err := invoke(t, m, env, "browser_scroll", `{"delta_x":20000,"delta_y":0}`)
	var he *host.Error
	if !errors.As(err, &he) || he.Code != "invalid_args" {
		t.Fatalf("oversized scroll = %v", err)
	}
	if len(appr.requests()) != 3 {
		t.Fatalf("approvals = %d, want 3", len(appr.requests()))
	}
}
