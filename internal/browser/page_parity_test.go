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

func toolErrorCode(t *testing.T, err error) string {
	t.Helper()
	var he *host.Error
	if !errors.As(err, &he) {
		t.Fatalf("err = %v, want *host.Error", err)
	}
	return he.Code
}

// rejectedBeforePolicy asserts a call is invalid_args with no approval and no
// protocol method: the tool refused it before it reached the module policy.
func rejectedBeforePolicy(t *testing.T, tool, args string) {
	t.Helper()
	f := newFake()
	appr := &fakeApprovals{answer: host.ApprovalAllowOnce}
	cfg := moduleConfig()
	m := newModule(t.Context(), t, &cfg, Deps{Dial: dialTo(f), Approvals: appr})
	_, err := invoke(t, m, host.Env{CallID: "x1"}, tool, args)
	if got := toolErrorCode(t, err); got != "invalid_args" {
		t.Fatalf("%s code = %s, want invalid_args", tool, got)
	}
	if len(appr.requests()) != 0 || len(f.calls) != 0 {
		t.Fatalf("%s reached policy: approvals=%d calls=%v", tool, len(appr.requests()), f.calls)
	}
}

// TestF6TOOL006BrowserBack (TOOL-006): back walks exactly one history entry,
// needs allow-once approval, reports an empty history as a typed error, and
// never follows a history target the origin policy refuses.
func TestF6TOOL006BrowserBack(t *testing.T) {
	t.Parallel()
	backNeedsApproval(t)
	backWithoutHistory(t)
	backSelectsPrevious(t)
	backBlocksPrivate(t)
}

func backNeedsApproval(t *testing.T) {
	t.Helper()
	f := newFake()
	cfg := moduleConfig()
	m := newModule(t.Context(), t, &cfg, Deps{Dial: dialTo(f)})
	_, err := invoke(t, m, host.Env{}, "browser_back", `{}`)
	if got := toolErrorCode(t, err); got != "denied" {
		t.Fatalf("code = %s, want denied", got)
	}
	if f.called("Page.getNavigationHistory") {
		t.Fatalf("history was read without approval: %v", f.calls)
	}
}

func backWithoutHistory(t *testing.T) {
	t.Helper()
	f := newFake()
	f.results["Page.getNavigationHistory"] = json.RawMessage(`{"currentIndex":0,"entries":[]}`)
	appr := &fakeApprovals{answer: host.ApprovalAllowOnce}
	cfg := moduleConfig()
	m := newModule(t.Context(), t, &cfg, Deps{Dial: dialTo(f), Approvals: appr})
	_, err := invoke(t, m, host.Env{CallID: "b1"}, "browser_back", `{}`)
	if got := toolErrorCode(t, err); got != "invalid_args" {
		t.Fatalf("code = %s, want invalid_args", got)
	}
	if f.called("Page.navigateToHistoryEntry") {
		t.Fatalf("no-history back navigated: %v", f.calls)
	}
}

func backSelectsPrevious(t *testing.T) {
	t.Helper()
	f := newFake()
	f.results["Page.getNavigationHistory"] = json.RawMessage(`{"currentIndex":1,"entries":[{"id":4,"url":"https://app.example/a"},{"id":5,"url":"https://app.example/b"}]}`)
	var entryID int
	f.onCall = func(method string, params any) {
		if method != "Page.navigateToHistoryEntry" {
			return
		}
		if args, ok := params.(map[string]int); ok {
			entryID = args["entryId"]
		}
	}
	appr := &fakeApprovals{answer: host.ApprovalAllowOnce}
	cfg := moduleConfig()
	m := newModule(t.Context(), t, &cfg, Deps{Dial: dialTo(f), Approvals: appr})
	res, err := invoke(t, m, host.Env{CallID: "b2"}, "browser_back", `{}`)
	if err != nil {
		t.Fatalf("back: %v", err)
	}
	if entryID != 4 || !strings.Contains(res.Content, "https://app.example/a") {
		t.Fatalf("entry = %d, content = %q", entryID, res.Content)
	}
	if res.UntrustedSource != "browser:app.example" {
		t.Fatalf("taint = %q", res.UntrustedSource)
	}
}

func backBlocksPrivate(t *testing.T) {
	t.Helper()
	f := newFake()
	f.results["Page.getNavigationHistory"] = json.RawMessage(`{"currentIndex":1,"entries":[{"id":9,"url":"http://10.0.0.5/admin"},{"id":10,"url":"https://app.example/b"}]}`)
	appr := &fakeApprovals{answer: host.ApprovalAllowOnce}
	cfg := moduleConfig()
	cfg.LocalProfile = false
	m := newModule(t.Context(), t, &cfg, Deps{Dial: dialTo(f), Approvals: appr})
	_, err := invoke(t, m, host.Env{CallID: "b3"}, "browser_back", `{}`)
	if got := toolErrorCode(t, err); got != "blocked_host" {
		t.Fatalf("code = %s, want blocked_host", got)
	}
	if f.called("Page.navigateToHistoryEntry") {
		t.Fatalf("blocked history target was followed: %v", f.calls)
	}
}

// TestF6TOOL007BrowserClick (TOOL-007): a click can name a ref from the latest
// snapshot, and a stale or foreign ref is refused before approval or input.
func TestF6TOOL007BrowserClick(t *testing.T) {
	t.Parallel()
	t.Run("ref from the latest snapshot is clicked at its centre", func(t *testing.T) {
		t.Parallel()
		clickSnapshotRef(t)
	})
	t.Run("foreign ref is rejected before approval or input", func(t *testing.T) {
		t.Parallel()
		clickForeignRef(t)
	})
	t.Run("ref is stale after navigation", func(t *testing.T) {
		t.Parallel()
		clickStaleRef(t)
	})
	t.Run("coordinates still click without a snapshot", func(t *testing.T) {
		t.Parallel()
		clickCoordinates(t)
	})
	t.Run("mixing coordinates and ref is invalid", func(t *testing.T) {
		t.Parallel()
		rejectedBeforePolicy(t, "browser_click", `{"x":1,"y":2,"ref":7}`)
	})
}

const clickAXTree = `{"nodes":[{"role":{"value":"button"},"name":{"value":"Save"},"backendDOMNodeId":7}]}`

// clickModule builds a CDP module whose latest snapshot exposes ref 7.
func clickModule(t *testing.T) (*Module, *fakeTransport, *fakeApprovals) {
	t.Helper()
	f := newFake()
	f.results["Accessibility.getFullAXTree"] = json.RawMessage(clickAXTree)
	f.results["DOM.getBoxModel"] = json.RawMessage(`{"model":{"content":[0,0,10,0,10,20,0,20]}}`)
	appr := &fakeApprovals{answer: host.ApprovalAllowOnce}
	cfg := moduleConfig()
	return newModule(t.Context(), t, &cfg, Deps{Dial: dialTo(f), Approvals: appr}), f, appr
}

func clickSnapshotRef(t *testing.T) {
	t.Helper()
	m, f, appr := clickModule(t)
	if _, err := invoke(t, m, host.Env{}, "browser_snapshot", `{}`); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	var backend int
	var point struct{ X, Y float64 }
	f.onCall = func(method string, params any) {
		args, ok := params.(map[string]any)
		if !ok {
			return
		}
		switch method {
		case "DOM.getBoxModel":
			if v, ok := args["backendNodeId"].(int); ok {
				backend = v
			}
		case "Input.dispatchMouseEvent":
			if v, ok := args["x"].(float64); ok {
				point.X = v
			}
			if v, ok := args["y"].(float64); ok {
				point.Y = v
			}
		}
	}
	res, err := invoke(t, m, host.Env{CallID: "c1"}, "browser_click", `{"ref":7}`)
	if err != nil {
		t.Fatalf("click ref: %v", err)
	}
	if backend != 7 || point.X != 5 || point.Y != 10 {
		t.Fatalf("backend = %d, point = %+v", backend, point)
	}
	if !strings.Contains(res.Content, "ref 7") {
		t.Fatalf("content = %q", res.Content)
	}
	if len(appr.requests()) != 1 || appr.requests()[0].Effect != host.EffectNonIdempotent {
		t.Fatalf("approvals = %+v", appr.requests())
	}
}

func clickForeignRef(t *testing.T) {
	t.Helper()
	m, f, appr := clickModule(t)
	if _, err := invoke(t, m, host.Env{}, "browser_snapshot", `{}`); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	_, err := invoke(t, m, host.Env{CallID: "c2"}, "browser_click", `{"ref":99}`)
	if got := toolErrorCode(t, err); got != "invalid_args" {
		t.Fatalf("code = %s, want invalid_args", got)
	}
	if len(appr.requests()) != 0 || f.called("DOM.getBoxModel") || f.called("Input.dispatchMouseEvent") {
		t.Fatalf("foreign ref reached policy: approvals=%d calls=%v", len(appr.requests()), f.calls)
	}
}

func clickStaleRef(t *testing.T) {
	t.Helper()
	m, f, _ := clickModule(t)
	if _, err := invoke(t, m, host.Env{}, "browser_snapshot", `{}`); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	navEvents("https://app.example/next")(f)
	if _, err := invoke(t, m, host.Env{}, "browser_navigate", `{"url":"https://app.example/next"}`); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	_, err := invoke(t, m, host.Env{CallID: "c3"}, "browser_click", `{"ref":7}`)
	if got := toolErrorCode(t, err); got != "invalid_args" {
		t.Fatalf("code = %s, want invalid_args", got)
	}
	if f.called("DOM.getBoxModel") {
		t.Fatalf("stale ref reached the page: %v", f.calls)
	}
}

func clickCoordinates(t *testing.T) {
	t.Helper()
	m, f, _ := clickModule(t)
	if _, err := invoke(t, m, host.Env{CallID: "c4"}, "browser_click", `{"x":10,"y":20}`); err != nil {
		t.Fatalf("coordinate click: %v", err)
	}
	if !f.called("Input.dispatchMouseEvent") {
		t.Fatalf("calls = %v", f.calls)
	}
}

// TestF6TOOL010BrowserNavigate (TOOL-010): every non-public address class is
// refused before the page is called, including CGNAT and metadata endpoints,
// and a redirect to a private host is stopped.
func TestF6TOOL010BrowserNavigate(t *testing.T) {
	t.Parallel()
	blocked := []string{
		"http://127.0.0.1:8080/", "http://[::1]:8080/", "http://localhost:8080/",
		"http://10.0.0.5/", "http://172.16.0.1/", "http://192.168.1.1/",
		"http://169.254.169.254/latest/meta-data/", "http://100.64.0.1/",
		"http://100.100.100.200/", "http://[fd00:ec2::254]/", "http://0.0.0.0/",
		"http://[::ffff:10.0.0.5]/",
	}
	for _, raw := range blocked {
		forBlockedNavigation(t, raw)
	}
	for _, raw := range []string{"https://user@app.example/", "ftp://app.example/"} {
		forInvalidNavigation(t, raw)
	}
	t.Run("allowed origin is tainted", func(t *testing.T) {
		t.Parallel()
		navigateAllowedOrigin(t)
	})
	t.Run("redirect to a private host is stopped", func(t *testing.T) {
		t.Parallel()
		navigateRedirectBlocked(t)
	})
}

func navigateConfig() ModuleConfig {
	return ModuleConfig{
		Enabled: true, CDPURL: "ws://127.0.0.1:9222/devtools/page/x",
		AllowOrigins: []string{
			"https://app.example", "http://127.0.0.1:8080", "http://[::1]:8080",
			"http://localhost:8080", "http://10.0.0.5", "http://172.16.0.1",
			"http://192.168.1.1", "http://169.254.169.254", "http://100.64.0.1",
			"http://100.100.100.200", "http://[fd00:ec2::254]", "http://0.0.0.0",
			"http://[::ffff:10.0.0.5]",
		},
	}
}

func forBlockedNavigation(t *testing.T, raw string) {
	t.Helper()
	f := newFake()
	cfg := navigateConfig()
	m := newModule(t.Context(), t, &cfg, Deps{Dial: dialTo(f)})
	_, err := invoke(t, m, host.Env{}, "browser_navigate", `{"url":"`+raw+`"}`)
	if got := toolErrorCode(t, err); got != "blocked_host" {
		t.Fatalf("%s: code = %s, want blocked_host (%v)", raw, got, err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("%s reached the page: %v", raw, f.calls)
	}
}

func forInvalidNavigation(t *testing.T, raw string) {
	t.Helper()
	f := newFake()
	cfg := navigateConfig()
	m := newModule(t.Context(), t, &cfg, Deps{Dial: dialTo(f)})
	_, err := invoke(t, m, host.Env{}, "browser_navigate", `{"url":"`+raw+`"}`)
	if got := toolErrorCode(t, err); got != "invalid_args" {
		t.Fatalf("%s: code = %s, want invalid_args", raw, got)
	}
}

func navigateAllowedOrigin(t *testing.T) {
	t.Helper()
	f := newFake()
	navEvents("https://app.example/dashboard")(f)
	cfg := navigateConfig()
	m := newModule(t.Context(), t, &cfg, Deps{Dial: dialTo(f)})
	res, err := invoke(t, m, host.Env{}, "browser_navigate", `{"url":"https://app.example/"}`)
	if err != nil || res.UntrustedSource != "browser:app.example" {
		t.Fatalf("navigate = %+v, %v", res, err)
	}
}

func navigateRedirectBlocked(t *testing.T) {
	t.Helper()
	f := newFake()
	navEvents("http://169.254.169.254/latest/meta-data/")(f)
	cfg := navigateConfig()
	m := newModule(t.Context(), t, &cfg, Deps{Dial: dialTo(f)})
	_, err := invoke(t, m, host.Env{}, "browser_navigate", `{"url":"https://app.example/go"}`)
	if got := toolErrorCode(t, err); got != "blocked_host" {
		t.Fatalf("code = %s, want blocked_host", got)
	}
	if !f.called("Page.stopLoading") {
		t.Fatalf("redirect load was not stopped: %v", f.calls)
	}
}

// TestF6TOOL011BrowserPress (TOOL-011): a known key is dispatched as keyDown
// and keyUp, while an unknown key is invalid_args before approval or input.
func TestF6TOOL011BrowserPress(t *testing.T) {
	t.Parallel()
	t.Run("known key is dispatched", func(t *testing.T) {
		t.Parallel()
		pressKnownKey(t)
	})
	t.Run("unknown key is invalid before approval", func(t *testing.T) {
		t.Parallel()
		rejectedBeforePolicy(t, "browser_press", `{"key":"F13"}`)
	})
	t.Run("denied key never reaches the page", func(t *testing.T) {
		t.Parallel()
		pressDenied(t)
	})
}

func pressKnownKey(t *testing.T) {
	t.Helper()
	f := newFake()
	var keys []string
	f.onCall = func(method string, params any) {
		if method != "Input.dispatchKeyEvent" {
			return
		}
		if args, ok := params.(map[string]string); ok {
			keys = append(keys, args["type"]+":"+args["key"])
		}
	}
	appr := &fakeApprovals{answer: host.ApprovalAllowOnce}
	cfg := moduleConfig()
	m := newModule(t.Context(), t, &cfg, Deps{Dial: dialTo(f), Approvals: appr})
	if _, err := invoke(t, m, host.Env{CallID: "p1"}, "browser_press", `{"key":"Enter"}`); err != nil {
		t.Fatalf("press: %v", err)
	}
	if strings.Join(keys, ",") != "keyDown:Enter,keyUp:Enter" {
		t.Fatalf("keys = %v", keys)
	}
	if len(appr.requests()) != 1 {
		t.Fatalf("approvals = %d", len(appr.requests()))
	}
}

func pressDenied(t *testing.T) {
	t.Helper()
	f := newFake()
	appr := &fakeApprovals{answer: host.ApprovalDeny}
	cfg := moduleConfig()
	m := newModule(t.Context(), t, &cfg, Deps{Dial: dialTo(f), Approvals: appr})
	_, err := invoke(t, m, host.Env{CallID: "p3"}, "browser_press", `{"key":"Tab"}`)
	if got := toolErrorCode(t, err); got != "denied" {
		t.Fatalf("code = %s, want denied", got)
	}
	if f.called("Input.dispatchKeyEvent") {
		t.Fatalf("denied key dispatched: %v", f.calls)
	}
}

// TestF6TOOL012BrowserScroll (TOOL-012): the wheel delta is clamped to the
// document range, and a missing layout is an explicit error instead of a
// silently ineffective scroll.
func TestF6TOOL012BrowserScroll(t *testing.T) {
	t.Parallel()
	t.Run("delta is clamped to the document edge", func(t *testing.T) {
		t.Parallel()
		scrollClamped(t, `{"x":10,"y":90,"maxX":20,"maxY":100}`, 50, 50, 10, 10)
	})
	t.Run("negative delta stops at the top", func(t *testing.T) {
		t.Parallel()
		scrollClamped(t, `{"x":0,"y":0,"maxX":20,"maxY":100}`, -50, -50, 0, 0)
	})
	t.Run("missing layout is a clear error", func(t *testing.T) {
		t.Parallel()
		scrollMissingLayout(t)
	})
	t.Run("out-of-range delta is invalid before approval", func(t *testing.T) {
		t.Parallel()
		rejectedBeforePolicy(t, "browser_scroll", `{"delta_x":20000,"delta_y":0}`)
	})
}

func scrollClamped(t *testing.T, layout string, dx, dy, wantX, wantY float64) {
	t.Helper()
	f := newFake()
	var last map[string]any
	f.reply = func(method string, params any) (json.RawMessage, bool) {
		args, ok := params.(map[string]any)
		if !ok {
			return nil, false
		}
		if method == "Input.dispatchMouseEvent" {
			last = args
			return nil, false
		}
		expr, ok := args["expression"].(string)
		if !ok {
			return nil, false
		}
		if method == "Runtime.evaluate" && strings.Contains(expr, "scrollingElement") {
			return json.RawMessage(`{"result":{"value":` + layout + `}}`), true
		}
		return nil, false
	}
	appr := &fakeApprovals{answer: host.ApprovalAllowOnce}
	cfg := moduleConfig()
	m := newModule(t.Context(), t, &cfg, Deps{Dial: dialTo(f), Approvals: appr})
	args := `{"delta_x":` + jsonNum(dx) + `,"delta_y":` + jsonNum(dy) + `}`
	if _, err := invoke(t, m, host.Env{CallID: "s1"}, "browser_scroll", args); err != nil {
		t.Fatalf("scroll: %v", err)
	}
	if last["deltaX"] != wantX || last["deltaY"] != wantY {
		t.Fatalf("dispatched = %+v, want %v/%v", last, wantX, wantY)
	}
}

func jsonNum(v float64) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "0"
	}
	return string(b)
}

func scrollMissingLayout(t *testing.T) {
	t.Helper()
	f := newFake()
	f.results["Runtime.evaluate"] = json.RawMessage(`{"result":{"value":null}}`)
	appr := &fakeApprovals{answer: host.ApprovalAllowOnce}
	cfg := moduleConfig()
	m := newModule(t.Context(), t, &cfg, Deps{Dial: dialTo(f), Approvals: appr})
	_, err := invoke(t, m, host.Env{CallID: "s3"}, "browser_scroll", `{"delta_x":1,"delta_y":1}`)
	if got := toolErrorCode(t, err); got != "network" || !strings.Contains(err.Error(), "scroll layout") {
		t.Fatalf("err = %v, want network with scroll layout", err)
	}
	if f.called("Input.dispatchMouseEvent") {
		t.Fatalf("scroll without layout dispatched: %v", f.calls)
	}
}

// TestF6TOOL013BrowserSnapshot (TOOL-013): the accessibility tree keeps
// stable refs, redacts the names of sensitive controls, and honours the
// result bound.
func TestF6TOOL013BrowserSnapshot(t *testing.T) {
	t.Parallel()
	t.Run("redacts sensitive names and keeps stable refs", func(t *testing.T) {
		t.Parallel()
		snapshotRedacts(t)
	})
	t.Run("result bound is enforced", func(t *testing.T) {
		t.Parallel()
		snapshotBound(t)
	})
}

func snapshotRedacts(t *testing.T) {
	t.Helper()
	f := newFake()
	tree := `{"nodes":[` +
		`{"role":{"value":"button"},"name":{"value":"Save"},"backendDOMNodeId":7},` +
		`{"role":{"value":"textbox"},"name":{"value":"Account"},"backendDOMNodeId":8},` +
		`{"role":{"value":"searchbox"},"name":{"value":"Query"},"backendDOMNodeId":9},` +
		`{"role":{"value":"password"},"name":{"value":"hunter2"},"backendDOMNodeId":10},` +
		`{"role":{"value":"spinbutton"},"name":{"value":"PIN 1234"},"backendDOMNodeId":11}]}`
	f.results["Accessibility.getFullAXTree"] = json.RawMessage(tree)
	cfg := moduleConfig()
	m := newModule(t.Context(), t, &cfg, Deps{Dial: dialTo(f)})
	first, err := invoke(t, m, host.Env{}, "browser_snapshot", `{}`)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	for _, leaked := range []string{"Account", "Query", "hunter2", "PIN 1234"} {
		if strings.Contains(first.Content, leaked) {
			t.Fatalf("snapshot leaked %q: %s", leaked, first.Content)
		}
	}
	if !strings.Contains(first.Content, `"ref":7`) || !strings.Contains(first.Content, "Save") {
		t.Fatalf("snapshot lacks the button: %s", first.Content)
	}
	var nodes []map[string]any
	if uerr := json.Unmarshal([]byte(first.Content), &nodes); uerr != nil {
		t.Fatalf("snapshot JSON: %v", uerr)
	}
	for _, node := range nodes {
		role, ok := node["role"].(string)
		if !ok {
			t.Fatalf("snapshot node without a role: %+v", node)
		}
		if sensitiveRole(role) {
			if _, ok := node["name"]; ok {
				t.Fatalf("sensitive node kept a name: %+v", node)
			}
		}
	}
	second, err := invoke(t, m, host.Env{}, "browser_snapshot", `{}`)
	if err != nil || second.Content != first.Content {
		t.Fatalf("refs are not stable: %v", err)
	}
}

func snapshotBound(t *testing.T) {
	t.Helper()
	small := mustPage(t, newFake(), Config{AllowOrigins: []string{"https://app.example"}, MaxResult: 1})
	if _, err := small.Snapshot(context.Background()); !errors.Is(err, errTooLarge) {
		t.Fatalf("snapshot bound err = %v, want errTooLarge", err)
	}
}

// TestF6TOOL014BrowserType (TOOL-014): approved text reaches the focused
// element, while the typed value never appears in the approval request (the
// only event the module emits), the preview, or the tool result.
func TestF6TOOL014BrowserType(t *testing.T) {
	t.Parallel()
	t.Run("approval hides the text", func(t *testing.T) {
		t.Parallel()
		typeApprovedHidesText(t)
	})
	t.Run("denied text never reaches the page", func(t *testing.T) {
		t.Parallel()
		typeDenied(t)
	})
	t.Run("effect is non_idempotent", func(t *testing.T) {
		t.Parallel()
		typeEffect(t)
	})
}

const typedSecret = "s3cret-hunter2"

func typeApprovedHidesText(t *testing.T) {
	t.Helper()
	f := newFake()
	var inserted string
	f.onCall = func(method string, params any) {
		if method != "Input.insertText" {
			return
		}
		if args, ok := params.(map[string]string); ok {
			inserted = args["text"]
		}
	}
	appr := &fakeApprovals{answer: host.ApprovalAllowOnce}
	cfg := moduleConfig()
	m := newModule(t.Context(), t, &cfg, Deps{Dial: dialTo(f), Approvals: appr})
	res, err := invoke(t, m, host.Env{CallID: "t1"}, "browser_type", `{"text":"`+typedSecret+`"}`)
	if err != nil {
		t.Fatalf("type: %v", err)
	}
	if inserted != typedSecret {
		t.Fatalf("inserted = %q", inserted)
	}
	if strings.Contains(res.Content, typedSecret) {
		t.Fatalf("result leaked the text: %q", res.Content)
	}
	reqs := appr.requests()
	if len(reqs) != 1 {
		t.Fatalf("approvals = %d", len(reqs))
	}
	req := reqs[0]
	if strings.Contains(req.Preview, typedSecret) || strings.Contains(string(req.Args), typedSecret) {
		t.Fatalf("approval request leaked the text: preview=%q args=%q", req.Preview, req.Args)
	}
	if len(req.Args) != 0 || req.Effect != host.EffectNonIdempotent {
		t.Fatalf("args/effect = %q/%q", req.Args, req.Effect)
	}
}

func typeDenied(t *testing.T) {
	t.Helper()
	f := newFake()
	appr := &fakeApprovals{answer: host.ApprovalDeny}
	cfg := moduleConfig()
	m := newModule(t.Context(), t, &cfg, Deps{Dial: dialTo(f), Approvals: appr})
	_, err := invoke(t, m, host.Env{CallID: "t2"}, "browser_type", `{"text":"`+typedSecret+`"}`)
	if got := toolErrorCode(t, err); got != "denied" {
		t.Fatalf("code = %s, want denied", got)
	}
	if f.called("Input.insertText") {
		t.Fatalf("denied text reached the page: %v", f.calls)
	}
}

func typeEffect(t *testing.T) {
	t.Helper()
	for _, tool := range Tools() {
		if tool.Name != "browser_type" {
			continue
		}
		if tool.Effect != "non_idempotent" {
			t.Fatalf("effect = %q", tool.Effect)
		}
		return
	}
	t.Fatal("browser_type is not registered")
}
