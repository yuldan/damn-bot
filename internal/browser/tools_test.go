//go:build !minimal

package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
	if !strings.Contains(res.Content, `"ref":7`) || !strings.Contains(res.Content, `"role":"button"`) {
		t.Fatalf("snapshot content = %s", res.Content)
	}
	if strings.Contains(res.Content, "Password") {
		t.Fatalf("snapshot leaked a node name: %s", res.Content)
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

func TestWaitForTruthyAndTimeout(t *testing.T) {
	t.Parallel()
	f := newFake()
	f.results["Runtime.evaluate"] = json.RawMessage(`{"result":{"value":true}}`)
	cfg := moduleConfig()
	m := newModule(t.Context(), t, &cfg, Deps{Dial: dialTo(f)})
	if _, err := invoke(t, m, host.Env{}, "browser_wait_for", `{"expression":"ready","timeout_ms":10}`); err != nil {
		t.Fatalf("wait_for: %v", err)
	}
	f.results["Runtime.evaluate"] = json.RawMessage(`{"result":{"value":false}}`)
	_, err := invoke(t, m, host.Env{}, "browser_wait_for", `{"expression":"never","timeout_ms":1}`)
	var he *host.Error
	if !errors.As(err, &he) || he.Code != "timeout" {
		t.Fatalf("wait_for timeout = %v, want timeout", err)
	}
}

func TestPDFBounds(t *testing.T) {
	t.Parallel()
	f := newFake()
	f.results["Page.printToPDF"] = json.RawMessage(`{"data":"` + base64.StdEncoding.EncodeToString([]byte("pdf")) + `"}`)
	cfg := moduleConfig()
	m := newModule(t.Context(), t, &cfg, Deps{Dial: dialTo(f)})
	res, err := invoke(t, m, host.Env{}, "browser_pdf", `{}`)
	if err != nil || !strings.Contains(res.Content, "pdf 3 bytes") {
		t.Fatalf("pdf = %+v, %v", res, err)
	}
	if got := res.Meta["bytes"]; got != 3 {
		t.Fatalf("meta bytes = %v", got)
	}
}

func TestPressSelectHoverDragScroll(t *testing.T) {
	t.Parallel()
	appr := &fakeApprovals{answer: host.ApprovalAllowOnce}
	f := newFake()
	f.results["Runtime.evaluate"] = json.RawMessage(`{"result":{"value":true}}`)
	cfg := moduleConfig()
	m := newModule(t.Context(), t, &cfg, Deps{Dial: dialTo(f), Approvals: appr})
	env := host.Env{CallID: "c1"}
	if _, err := invoke(t, m, env, "browser_press", `{"key":"Enter"}`); err != nil {
		t.Fatalf("press: %v", err)
	}
	if _, err := invoke(t, m, env, "browser_hover", `{"x":1,"y":2}`); err != nil {
		t.Fatalf("hover: %v", err)
	}
	if _, err := invoke(t, m, env, "browser_drag", `{"x":1,"y":2,"end_x":3,"end_y":4}`); err != nil {
		t.Fatalf("drag: %v", err)
	}
	if _, err := invoke(t, m, env, "browser_scroll", `{"x":0,"y":0,"delta_x":1,"delta_y":2}`); err != nil {
		t.Fatalf("scroll: %v", err)
	}
	if _, err := invoke(t, m, env, "browser_select", `{"selector":"#s","value":"v"}`); err != nil {
		t.Fatalf("select: %v", err)
	}
	if !strings.Contains(strings.Join(f.calls, ","), "Input.dispatchKeyEvent") ||
		!strings.Contains(strings.Join(f.calls, ","), "Input.dispatchMouseEvent") {
		t.Fatalf("calls = %v", f.calls)
	}
	_, err := invoke(t, m, env, "browser_press", `{"key":"F13"}`)
	var he *host.Error
	if !errors.As(err, &he) || he.Code != "invalid_args" {
		t.Fatalf("unknown key = %v", err)
	}
	_, err = invoke(t, m, env, "browser_scroll", `{"delta_x":20000,"delta_y":0}`)
	if !errors.As(err, &he) || he.Code != "invalid_args" {
		t.Fatalf("oversized scroll = %v", err)
	}
	previews := appr.requests()
	if len(previews) != 5 {
		t.Fatalf("approvals = %d, want 5", len(previews))
	}
	for _, req := range previews {
		if strings.Contains(req.Preview, `"v"`) {
			t.Fatalf("select preview leaked the value: %q", req.Preview)
		}
	}
}

func TestUploadWorkspaceBounds(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "doc.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	appr := &fakeApprovals{answer: host.ApprovalAllowOnce}
	f := newFake()
	f.results["Runtime.evaluate"] = json.RawMessage(`{"result":{"value":true}}`)
	cfg := moduleConfig()
	m := newModule(t.Context(), t, &cfg, Deps{Dial: dialTo(f), Approvals: appr})
	res, err := invoke(t, m, host.Env{Cwd: dir}, "browser_upload", `{"selector":"input","path":"doc.txt"}`)
	if err != nil || !strings.Contains(res.Content, "uploaded doc.txt (5 bytes)") {
		t.Fatalf("upload = %+v, %v", res, err)
	}
	if !f.called("Runtime.evaluate") {
		t.Fatalf("calls = %v", f.calls)
	}
	_, err = invoke(t, m, host.Env{Cwd: dir}, "browser_upload", `{"selector":"input","path":"../escape"}`)
	var he *host.Error
	if !errors.As(err, &he) || he.Code != "invalid_args" {
		t.Fatalf("escaping upload = %v", err)
	}
}

func TestDownloadWritesWorkspace(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	appr := &fakeApprovals{answer: host.ApprovalAllowOnce}
	f := newFake()
	blob := base64.StdEncoding.EncodeToString([]byte("payload"))
	f.results["Runtime.evaluate"] = json.RawMessage(`{"result":{"value":"` + blob + `"}}`)
	cfg := moduleConfig()
	m := newModule(t.Context(), t, &cfg, Deps{Dial: dialTo(f), Approvals: appr})
	res, err := invoke(t, m, host.Env{Cwd: dir}, "browser_download", `{"url":"https://app.example/file.bin","path":"out.bin"}`)
	if err != nil || !strings.Contains(res.Content, "downloaded 7 bytes") {
		t.Fatalf("download = %+v, %v", res, err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "out.bin")) // #nosec G304 -- test workspace path
	if err != nil || string(got) != "payload" {
		t.Fatalf("file = %q, %v", got, err)
	}
	if res.Meta["name"] != "file.bin" {
		t.Fatalf("meta name = %v", res.Meta["name"])
	}
	f.results["Runtime.evaluate"] = json.RawMessage(`{"result":{"value":"` + base64.StdEncoding.EncodeToString(make([]byte, maxWorkspaceBlob+1)) + `"}}`)
	_, err = invoke(t, m, host.Env{Cwd: dir}, "browser_download", `{"url":"https://app.example/big","path":"big.bin"}`)
	var he *host.Error
	if !errors.As(err, &he) || he.Code != "too_large" {
		t.Fatalf("oversize download = %v, want too_large", err)
	}
	if _, serr := os.Stat(filepath.Join(dir, "big.bin")); serr == nil {
		t.Fatal("oversize download left a file")
	}
}
