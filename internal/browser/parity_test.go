//go:build !minimal

package browser

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	stdimage "image"
	"image/png"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/damnbot/damnbot/internal/modules/host"
)

// failingTransport fails one method so the error path of a parity read is
// exercised without touching the fake transport other tests share.
type failingTransport struct {
	Transport
	method string
	err    error
}

func (f failingTransport) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if method == f.method {
		return nil, f.err
	}
	raw, err := f.Transport.Call(ctx, method, params)
	if err != nil {
		return nil, fmt.Errorf("browser test transport: %w", err)
	}
	return raw, nil
}

func consoleResult(t *testing.T, content string) int {
	t.Helper()
	if content == "no console messages" {
		return 0
	}
	return strings.Count(content, "\n") + 1
}

// TestF6TOOL008BrowserConsole (TOOL-008): browser_console only reads the
// bounded console buffer, never evaluates page script, and taints the result.
func TestF6TOOL008BrowserConsole(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		run  func(*testing.T)
	}{
		{"reads buffered events without evaluating", consoleReadsBuffered},
		{"buffer is bounded", consoleBufferBounded},
		{"negative control: one entry is clipped", consoleEntryClipped},
		{"enable failure is a typed error", consoleEnableFailure},
		{"engine page without a CDP transport is unsupported", consoleUnsupportedEngine},
	}
	for _, tc := range cases {
		t.Run(tc.name, tc.run)
	}
}

// consoleReadsBuffered proves the three console sources are read without
// evaluating page script.
func consoleReadsBuffered(t *testing.T) {
	t.Helper()
	f := newFake()
	emit(f, "Runtime.consoleAPICalled", `{"type":"log","args":[{"type":"string","value":"hello"}]}`)
	emit(f, "Log.entryAdded", `{"entry":{"level":"warning","text":"careful"}}`)
	emit(f, "Runtime.exceptionThrown", `{"exceptionDetails":{"text":"boom"}}`)
	m := newModule(t.Context(), t, new(moduleConfig()), Deps{Dial: dialTo(f)})
	res, err := m.Invoke(t.Context(), "browser_console", json.RawMessage(`{}`), host.Env{})
	if err != nil {
		t.Fatalf("console: %v", err)
	}
	for _, want := range []string{`[log] "hello"`, "[warning] careful", "[exception] boom"} {
		if !strings.Contains(res.Content, want) {
			t.Fatalf("content = %q, want %q", res.Content, want)
		}
	}
	if res.UntrustedSource != "browser:page" {
		t.Fatalf("taint = %q", res.UntrustedSource)
	}
	for _, method := range []string{"Runtime.enable", "Log.enable"} {
		if !f.called(method) {
			t.Fatalf("calls = %v, want %s", f.calls, method)
		}
	}
	if f.called("Runtime.evaluate") {
		t.Fatalf("console evaluated page script: %v", f.calls)
	}
}

// consoleBufferBounded proves the ring keeps at most maxConsoleLines entries.
func consoleBufferBounded(t *testing.T) {
	t.Helper()
	f := newFake()
	m := newModule(t.Context(), t, new(moduleConfig()), Deps{Dial: dialTo(f)})
	for range 20 {
		for range 16 {
			emit(f, "Runtime.consoleAPICalled", `{"type":"log","args":[{"type":"string","value":"line"}]}`)
		}
		if _, err := m.Invoke(t.Context(), "browser_console", json.RawMessage(`{}`), host.Env{}); err != nil {
			t.Fatalf("console: %v", err)
		}
	}
	res, err := m.Invoke(t.Context(), "browser_console", json.RawMessage(`{}`), host.Env{})
	if err != nil {
		t.Fatalf("console: %v", err)
	}
	if got := consoleResult(t, res.Content); got > maxConsoleLines {
		t.Fatalf("buffer kept %d lines > %d", got, maxConsoleLines)
	}
}

// consoleEntryClipped proves one oversized entry is clipped, not dropped.
func consoleEntryClipped(t *testing.T) {
	t.Helper()
	f := newFake()
	emit(f, "Runtime.consoleAPICalled", `{"type":"log","args":[{"type":"string","value":"`+strings.Repeat("x", 5000)+`"}]}`)
	m := newModule(t.Context(), t, new(moduleConfig()), Deps{Dial: dialTo(f)})
	res, err := m.Invoke(t.Context(), "browser_console", json.RawMessage(`{}`), host.Env{})
	if err != nil {
		t.Fatalf("console: %v", err)
	}
	if n := len([]rune(res.Content)); n > maxConsoleLine+len("[log] ")+2 {
		t.Fatalf("entry kept %d runes, want at most %d", n, maxConsoleLine)
	}
}

// consoleEnableFailure proves a failed domain enable is a typed network error.
func consoleEnableFailure(t *testing.T) {
	t.Helper()
	f := newFake()
	broken := failingTransport{Transport: f, method: "Runtime.enable", err: errors.New("socket closed")}
	m := newModule(t.Context(), t, new(moduleConfig()), Deps{Dial: dialTo(broken)})
	_, err := m.Invoke(t.Context(), "browser_console", json.RawMessage(`{}`), host.Env{})
	var he *host.Error
	if !errors.As(err, &he) || he.Code != "network" {
		t.Fatalf("err = %v, want network", err)
	}
}

// consoleUnsupportedEngine proves an engine page has no CDP console transport.
func consoleUnsupportedEngine(t *testing.T) {
	t.Helper()
	p, err := NewPage(stubEngine{}, Config{AllowOrigins: []string{"https://app.example"}})
	if err != nil {
		t.Fatalf("NewPage: %v", err)
	}
	if _, err := p.Console(context.Background()); !errors.Is(err, errUnsupported) {
		t.Fatalf("Console = %v, want errUnsupported", err)
	}
}

// TestF6TOOL009BrowserGetImages (TOOL-009): the image list is bounded, carries
// the natural size, and every result is tainted.
func TestF6TOOL009BrowserGetImages(t *testing.T) {
	t.Parallel()
	t.Run("lists images with size", func(t *testing.T) {
		t.Parallel()
		imagesListed(t)
	})
	t.Run("result is capped at maxImages", func(t *testing.T) {
		t.Parallel()
		imagesCapped(t)
	})
	t.Run("negative control: empty page", func(t *testing.T) {
		t.Parallel()
		imagesEmpty(t)
	})
	t.Run("negative control: a non-array reply fails", func(t *testing.T) {
		t.Parallel()
		imagesNonArray(t)
	})
}

// imagesReply builds one Runtime.evaluate reply whose value is the JSON text.
func imagesReply(t *testing.T, value string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"result": map[string]any{"value": json.RawMessage(value)}})
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return raw
}

// imagesListed proves the natural size, alt text and taint reach the result.
func imagesListed(t *testing.T) {
	t.Helper()
	f := newFake()
	f.results["Runtime.evaluate"] = imagesReply(t, `[{"src":"https://app.example/a.png","alt":"logo","width":10,"height":20}]`)
	m := newModule(t.Context(), t, new(moduleConfig()), Deps{Dial: dialTo(f)})
	res, err := m.Invoke(t.Context(), "browser_get_images", json.RawMessage(`{}`), host.Env{})
	if err != nil {
		t.Fatalf("get_images: %v", err)
	}
	for _, want := range []string{"https://app.example/a.png", "(10x20)", `alt="logo"`} {
		if !strings.Contains(res.Content, want) {
			t.Fatalf("content = %q, want %q", res.Content, want)
		}
	}
	if res.UntrustedSource != "browser:page" {
		t.Fatalf("taint = %q", res.UntrustedSource)
	}
	if got := res.Meta["images"]; got != 1 {
		t.Fatalf("meta images = %v", got)
	}
}

// imagesCapped proves a long page list never exceeds maxImages entries.
func imagesCapped(t *testing.T) {
	t.Helper()
	var many []string
	for i := range 150 {
		many = append(many, `{"src":"https://app.example/`+strings.Repeat("i", i%3)+`.png","width":1,"height":1}`)
	}
	f := newFake()
	f.results["Runtime.evaluate"] = imagesReply(t, "["+strings.Join(many, ",")+"]")
	m := newModule(t.Context(), t, new(moduleConfig()), Deps{Dial: dialTo(f)})
	res, err := m.Invoke(t.Context(), "browser_get_images", json.RawMessage(`{}`), host.Env{})
	if err != nil {
		t.Fatalf("get_images: %v", err)
	}
	if got := consoleResult(t, res.Content); got > maxImages {
		t.Fatalf("listed %d images > %d", got, maxImages)
	}
}

// imagesEmpty proves a page without images reports the empty list.
func imagesEmpty(t *testing.T) {
	t.Helper()
	f := newFake()
	f.results["Runtime.evaluate"] = imagesReply(t, `[]`)
	m := newModule(t.Context(), t, new(moduleConfig()), Deps{Dial: dialTo(f)})
	res, err := m.Invoke(t.Context(), "browser_get_images", json.RawMessage(`{}`), host.Env{})
	if err != nil || res.Content != "no images on the page" {
		t.Fatalf("get_images = %q, %v", res.Content, err)
	}
}

// imagesNonArray proves a non-array evaluate value fails instead of decoding.
func imagesNonArray(t *testing.T) {
	t.Helper()
	f := newFake()
	f.results["Runtime.evaluate"] = imagesReply(t, `{"not":"an array"}`)
	m := newModule(t.Context(), t, new(moduleConfig()), Deps{Dial: dialTo(f)})
	if _, err := m.Invoke(t.Context(), "browser_get_images", json.RawMessage(`{}`), host.Env{}); err == nil {
		t.Fatal("non-array image reply succeeded")
	}
}

// stubEngine is the smallest Engine: enough to prove the parity reads fail
// closed on an engine page instead of reaching for a CDP transport.
type stubEngine struct{}

func (stubEngine) Navigate(_ context.Context, rawURL string, _ func(*url.URL) error) (string, error) {
	return rawURL, nil
}

func (stubEngine) Read(context.Context, ReadOp) (Reply, error) {
	return Reply{Value: json.RawMessage("null")}, nil
}

func (stubEngine) Act(context.Context, *Action) error { return nil }

func (stubEngine) Close() error { return nil }

// visionPNG builds a real PNG so the host attachment validation sees honest
// magic bytes instead of a fixture string.
func visionPNG(t *testing.T) []byte {
	t.Helper()
	img := stdimage.NewNRGBA(stdimage.Rect(0, 0, 4, 4))
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 255
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png: %v", err)
	}
	return buf.Bytes()
}

func screenshotReply(t *testing.T, data []byte) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"data": base64.StdEncoding.EncodeToString(data)})
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return raw
}

// TestF6TOOL016BrowserVision (TOOL-016): the screenshot travels as a bounded
// image attachment for the vision route, never as base64 inside Content.
func TestF6TOOL016BrowserVision(t *testing.T) {
	t.Parallel()
	pngBytes := visionPNG(t)
	t.Run("attachment carries the screenshot", func(t *testing.T) {
		t.Parallel()
		visionAttachment(t, pngBytes)
	})
	t.Run("negative control: oversize screenshot is rejected", func(t *testing.T) {
		t.Parallel()
		visionOversize(t, pngBytes)
	})
	t.Run("negative control: unknown argument", func(t *testing.T) {
		t.Parallel()
		visionUnknownArgument(t)
	})
}

// visionAttachment proves the screenshot travels as one valid PNG attachment
// and never as base64 inside Content.
func visionAttachment(t *testing.T, pngBytes []byte) {
	t.Helper()
	f := newFake()
	f.results["Page.captureScreenshot"] = screenshotReply(t, pngBytes)
	m := newModule(t.Context(), t, new(moduleConfig()), Deps{Dial: dialTo(f)})
	res, err := m.Invoke(t.Context(), "browser_vision", json.RawMessage(`{"question":"what is shown?"}`), host.Env{})
	if err != nil {
		t.Fatalf("vision: %v", err)
	}
	if err := res.Validate(); err != nil {
		t.Fatalf("result invalid: %v", err)
	}
	if len(res.Attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(res.Attachments))
	}
	a := res.Attachments[0]
	if a.Kind != host.AttachmentImage || a.MIME != "image/png" || a.Name != visionAttachmentName {
		t.Fatalf("attachment = %+v", a)
	}
	if !bytes.Equal(a.Data, pngBytes) {
		t.Fatal("attachment data differs from the screenshot")
	}
	if res.UntrustedSource != "browser:page" || !strings.Contains(res.Content, "what is shown?") {
		t.Fatalf("result = %+v", res)
	}
	if strings.Contains(res.Content, base64.StdEncoding.EncodeToString(pngBytes)) {
		t.Fatal("content carries base64 image data")
	}
}

// visionOversize proves the MaxScreenshot cap rejects a too-large capture.
func visionOversize(t *testing.T, pngBytes []byte) {
	t.Helper()
	f := newFake()
	f.results["Page.captureScreenshot"] = screenshotReply(t, append(pngBytes, make([]byte, 2<<20)...))
	m := newModule(t.Context(), t, new(moduleConfig()), Deps{Dial: dialTo(f)})
	_, err := m.Invoke(t.Context(), "browser_vision", json.RawMessage(`{}`), host.Env{})
	var he *host.Error
	if !errors.As(err, &he) || he.Code != "too_large" {
		t.Fatalf("err = %v, want too_large", err)
	}
}

// visionUnknownArgument proves an argument outside the schema is rejected.
func visionUnknownArgument(t *testing.T) {
	t.Helper()
	f := newFake()
	m := newModule(t.Context(), t, new(moduleConfig()), Deps{Dial: dialTo(f)})
	if _, err := m.Invoke(t.Context(), "browser_vision", json.RawMessage(`{"path":"/etc/passwd"}`), host.Env{}); err == nil {
		t.Fatal("unknown argument accepted")
	}
}

// dialogConfirmEvent is the page-controlled confirm dialog every dialog case
// opens; its message must never reach an approval preview.
const dialogConfirmEvent = `{"type":"confirm","message":"delete everything?"}`

// TestF6TOOL018BrowserDialog (TOOL-018): prompt and confirm are dismissed by
// default, an allowed dialog needs one allow-once approval with a preview that
// never contains the page message, and dialog text is never executed.
func TestF6TOOL018BrowserDialog(t *testing.T) {
	t.Parallel()
	t.Run("default policy dismisses and answers nothing", func(t *testing.T) {
		t.Parallel()
		dialogDefaultDismiss(t)
	})
	t.Run("allowed dialog is accepted with approval once", func(t *testing.T) {
		t.Parallel()
		dialogAllowedAccept(t)
	})
	t.Run("negative control: denied approval answers nothing", func(t *testing.T) {
		t.Parallel()
		dialogDeniedApproval(t)
	})
	t.Run("negative control: unknown action", func(t *testing.T) {
		t.Parallel()
		dialogUnknownAction(t)
	})
	t.Run("policy keeps only configured types", func(t *testing.T) {
		t.Parallel()
		dialogPolicyClosed(t)
	})
}

// dialogDefaultDismiss proves the zero policy dismisses without approval.
func dialogDefaultDismiss(t *testing.T) {
	t.Helper()
	f := newFake()
	emit(f, "Page.javascriptDialogOpening", dialogConfirmEvent)
	appr := &fakeApprovals{answer: host.ApprovalAllowOnce}
	m := newModule(t.Context(), t, new(moduleConfig()), Deps{Dial: dialTo(f), Approvals: appr})
	res, err := m.Invoke(t.Context(), "browser_dialog", json.RawMessage(`{"action":"status"}`), host.Env{})
	if err != nil || res.Content != "no dialog is open" {
		t.Fatalf("status = %q, %v", res.Content, err)
	}
	if len(appr.requests()) != 0 {
		t.Fatalf("status asked for approval: %v", appr.requests())
	}
	if !f.called("Page.handleJavaScriptDialog") {
		t.Fatalf("dialog was not dismissed: %v", f.calls)
	}
	if f.called("Runtime.evaluate") {
		t.Fatalf("dialog text was executed: %v", f.calls)
	}
}

// dialogAllowedAccept proves an allowed dialog needs exactly one approval and
// the preview never carries the page message.
func dialogAllowedAccept(t *testing.T) {
	t.Helper()
	f := newFake()
	emit(f, "Page.javascriptDialogOpening", dialogConfirmEvent)
	appr := &fakeApprovals{answer: host.ApprovalAllowOnce}
	cfg := moduleConfig()
	cfg.DialogsAllow = []string{"confirm"}
	m := newModule(t.Context(), t, &cfg, Deps{Dial: dialTo(f), Approvals: appr})
	res, err := m.Invoke(t.Context(), "browser_dialog", json.RawMessage(`{"action":"status"}`), host.Env{})
	if err != nil || res.Content != "open confirm dialog: delete everything?" {
		t.Fatalf("status = %q, %v", res.Content, err)
	}
	res, err = m.Invoke(t.Context(), "browser_dialog", json.RawMessage(`{"action":"accept"}`), host.Env{CallID: "c9"})
	if err != nil || !strings.Contains(res.Content, "accepted the confirm dialog") {
		t.Fatalf("accept = %q, %v", res.Content, err)
	}
	reqs := appr.requests()
	if len(reqs) != 1 || reqs[0].Tool != "browser_dialog" || reqs[0].CallID != "c9" {
		t.Fatalf("approvals = %+v", reqs)
	}
	if strings.Contains(reqs[0].Preview, "delete everything?") {
		t.Fatalf("preview leaked the page message: %q", reqs[0].Preview)
	}
	if !f.called("Page.handleJavaScriptDialog") {
		t.Fatalf("calls = %v", f.calls)
	}
}

// dialogDeniedApproval proves a denied approval answers nothing.
func dialogDeniedApproval(t *testing.T) {
	t.Helper()
	f := newFake()
	emit(f, "Page.javascriptDialogOpening", dialogConfirmEvent)
	appr := &fakeApprovals{answer: host.ApprovalDeny}
	cfg := moduleConfig()
	cfg.DialogsAllow = []string{"confirm"}
	m := newModule(t.Context(), t, &cfg, Deps{Dial: dialTo(f), Approvals: appr})
	_, err := m.Invoke(t.Context(), "browser_dialog", json.RawMessage(`{"action":"dismiss"}`), host.Env{})
	var he *host.Error
	if !errors.As(err, &he) || he.Code != "denied" {
		t.Fatalf("err = %v, want denied", err)
	}
	if f.called("Page.handleJavaScriptDialog") {
		t.Fatalf("denied dialog still answered: %v", f.calls)
	}
}

// dialogUnknownAction proves an action outside the schema is rejected.
func dialogUnknownAction(t *testing.T) {
	t.Helper()
	f := newFake()
	m := newModule(t.Context(), t, new(moduleConfig()), Deps{Dial: dialTo(f)})
	if _, err := m.Invoke(t.Context(), "browser_dialog", json.RawMessage(`{"action":"always"}`), host.Env{}); err == nil {
		t.Fatal("unknown action accepted")
	}
}

// dialogPolicyClosed proves an unknown dialog type never reaches the page.
func dialogPolicyClosed(t *testing.T) {
	t.Helper()
	cfg := moduleConfig()
	cfg.DialogsAllow = []string{"nonsense"}
	if _, err := New(&cfg, Deps{}); !errors.Is(err, errBadConfig) {
		t.Fatalf("New = %v, want errBadConfig", err)
	}
}

// TestF6TOOL017BrowserCDP (TOOL-017): only reviewed methods pass, every call
// needs one allow-once approval, a session approval never widens it, and the
// preview exposes neither parameter values nor page content.
func TestF6TOOL017BrowserCDP(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		run  func(*testing.T)
	}{
		{"safe method runs after approval", cdpSafeMethodRun},
		{"sensitive method rejects a session approval", cdpSessionApprovalRejected},
		{"negative control: unreviewed method never reaches approval", cdpUnreviewedMethod},
		{"file upload is sensitive and its preview hides the path", cdpFileUpload},
		{"negative control: malformed parameters", cdpMalformedParams},
		{"method allowlist is closed", cdpAllowlistClosed},
	}
	for _, tc := range cases {
		t.Run(tc.name, tc.run)
	}
}

// cdpSafeMethodRun proves one read-only method runs after one approval.
func cdpSafeMethodRun(t *testing.T) {
	t.Helper()
	f := newFake()
	f.results["Page.getLayoutMetrics"] = json.RawMessage(`{"layoutViewport":{"clientWidth":800}}`)
	appr := &fakeApprovals{answer: host.ApprovalAllowOnce}
	m := newModule(t.Context(), t, new(moduleConfig()), Deps{Dial: dialTo(f), Approvals: appr})
	res, err := m.Invoke(t.Context(), "browser_cdp", json.RawMessage(`{"method":"Page.getLayoutMetrics"}`), host.Env{CallID: "c1"})
	if err != nil || !strings.Contains(res.Content, "clientWidth") || res.UntrustedSource != "browser:page" {
		t.Fatalf("cdp = %q, %v", res.Content, err)
	}
	reqs := appr.requests()
	if len(reqs) != 1 || reqs[0].Effect != host.EffectNonIdempotent || reqs[0].CallID != "c1" {
		t.Fatalf("approvals = %+v", reqs)
	}
	if !strings.Contains(reqs[0].Preview, "read-only") {
		t.Fatalf("preview does not name the class: %q", reqs[0].Preview)
	}
	if res.Meta["sensitive"] != false {
		t.Fatalf("meta = %v", res.Meta)
	}
}

// cdpSessionApprovalRejected proves a session approval never reaches a
// sensitive method.
func cdpSessionApprovalRejected(t *testing.T) {
	t.Helper()
	f := newFake()
	appr := &fakeApprovals{answer: host.ApprovalAllowSession}
	m := newModule(t.Context(), t, new(moduleConfig()), Deps{Dial: dialTo(f), Approvals: appr})
	_, err := m.Invoke(t.Context(), "browser_cdp", json.RawMessage(`{"method":"Network.getCookies"}`), host.Env{})
	var he *host.Error
	if !errors.As(err, &he) || he.Code != "denied" {
		t.Fatalf("err = %v, want denied", err)
	}
	if f.called("Network.getCookies") {
		t.Fatalf("session approval reached the method: %v", f.calls)
	}
}

// cdpUnreviewedMethod proves a method outside the allowlist asks for no
// approval and never reaches the transport.
func cdpUnreviewedMethod(t *testing.T) {
	t.Helper()
	f := newFake()
	appr := &fakeApprovals{answer: host.ApprovalAllowOnce}
	m := newModule(t.Context(), t, new(moduleConfig()), Deps{Dial: dialTo(f), Approvals: appr})
	for _, method := range []string{"Page.navigate", "Page.captureScreenshot", "Emulation.setDeviceMetricsOverride", "nonsense", "Runtime.evaluate(", "Network.setBypassCSP"} {
		_, err := m.Invoke(t.Context(), "browser_cdp", json.RawMessage(`{"method":"`+method+`"}`), host.Env{})
		var he *host.Error
		if !errors.As(err, &he) || he.Code != "denied" {
			t.Fatalf("%s: err = %v, want denied", method, err)
		}
	}
	if len(appr.requests()) != 0 {
		t.Fatalf("unreviewed methods asked for approval: %+v", appr.requests())
	}
	if len(f.calls) != 0 {
		t.Fatalf("unreviewed methods reached the transport: %v", f.calls)
	}
}

// cdpFileUpload proves a file: parameter is sensitive and its path never
// reaches the approval preview.
func cdpFileUpload(t *testing.T) {
	t.Helper()
	f := newFake()
	f.results["DOM.setFileInputFiles"] = json.RawMessage(`{}`)
	appr := &fakeApprovals{answer: host.ApprovalAllowOnce}
	m := newModule(t.Context(), t, new(moduleConfig()), Deps{Dial: dialTo(f), Approvals: appr})
	params := `{"nodeId":1,"files":["file:///etc/passwd"]}`
	if _, err := m.Invoke(t.Context(), "browser_cdp", json.RawMessage(`{"method":"DOM.setFileInputFiles","params":`+params+`}`), host.Env{}); err != nil {
		t.Fatalf("cdp: %v", err)
	}
	reqs := appr.requests()
	if len(reqs) != 1 {
		t.Fatalf("approvals = %+v", reqs)
	}
	if strings.Contains(reqs[0].Preview, "passwd") || strings.Contains(reqs[0].Preview, "file:") {
		t.Fatalf("preview leaked parameters: %q", reqs[0].Preview)
	}
}

// cdpMalformedParams proves undecodable parameters fail before any call.
func cdpMalformedParams(t *testing.T) {
	t.Helper()
	f := newFake()
	m := newModule(t.Context(), t, new(moduleConfig()), Deps{Dial: dialTo(f)})
	if _, err := m.Invoke(t.Context(), "browser_cdp", json.RawMessage(`{"method":"Page.getLayoutMetrics","params":{"x":`), host.Env{}); err == nil {
		t.Fatal("malformed parameters accepted")
	}
}

// cdpAllowlistClosed proves the reviewed allowlist stays a closed set.
func cdpAllowlistClosed(t *testing.T) {
	t.Helper()
	for method, want := range map[string]struct{ sensitive, allowed bool }{
		"Page.getLayoutMetrics":    {false, true},
		"Network.getCookies":       {true, true},
		"Storage.getCookies":       {true, true},
		"Debugger.enable":          {true, true},
		"Target.getTargets":        {true, true},
		"Browser.getVersion":       {true, true},
		"Page.navigate":            {false, false},
		"Runtime.consoleAPICalled": {false, false},
	} {
		sensitive, allowed := cdpMethodClass(method)
		if sensitive != want.sensitive || allowed != want.allowed {
			t.Errorf("%s: sensitive=%v allowed=%v, want %+v", method, sensitive, allowed, want)
		}
	}
}

// fakeProcess is a spawner-owned process with real pipes.
type fakeProcess struct {
	in    nopWriteCloser
	out   []byte
	done  chan struct{}
	waits int
}

type nopWriteCloser struct{ buf bytes.Buffer }

func (n *nopWriteCloser) Write(p []byte) (int, error) {
	written, err := n.buf.Write(p)
	if err != nil {
		return written, fmt.Errorf("browser test stdin: %w", err)
	}
	return written, nil
}
func (n *nopWriteCloser) Close() error { return nil }

func (p *fakeProcess) Stdio() (io.WriteCloser, io.ReadCloser, io.ReadCloser) {
	return &p.in, io.NopCloser(bytes.NewReader(p.out)), io.NopCloser(strings.NewReader(""))
}
func (p *fakeProcess) Done() <-chan struct{}  { return p.done }
func (p *fakeProcess) Wait() error            { p.waits++; return nil }
func (p *fakeProcess) Signal(os.Signal) error { return nil }
func (p *fakeProcess) Kill() error            { return nil }

// TestF6TOOL019BrowserExec (TOOL-019): browser_exec exists only for the
// browser-use backend, replaces every page tool, and fails closed without the
// CLI, the spawner or an approval.
func TestF6TOOL019BrowserExec(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		run  func(*testing.T)
	}{
		{"runs the helper through stdin", execRunsScript},
		{"replaces the page tools", execReplacesTools},
		{"negative control: no CLI or spawner", execNoCLI},
		{"negative control: denied approval never spawns", execDeniedApproval},
		{"negative control: exec is refused on the CDP engine", execOnCDPRefused},
		{"negative control: oversize output", execOversizeOutput},
	}
	for _, tc := range cases {
		t.Run(tc.name, tc.run)
	}
}

// execConfig returns a browser-use config with a throwaway absolute CLI path.
func execConfig(t *testing.T) ModuleConfig {
	t.Helper()
	cfg := moduleConfig()
	cfg.Engine = "browser-use"
	cfg.CDPURL = ""
	cfg.UseBin = filepath.Join(t.TempDir(), "browser-use")
	return cfg
}

// execRunsScript proves the program travels on stdin and only the backend name
// reaches the approval preview.
func execRunsScript(t *testing.T) {
	t.Helper()
	cfg := execConfig(t)
	proc := &fakeProcess{out: []byte("clicked #login"), done: make(chan struct{})}
	appr := &fakeApprovals{answer: host.ApprovalAllowOnce}
	var spawned *host.SpawnSpec
	m := newModule(t.Context(), t, &cfg, Deps{
		Approvals: appr,
		Spawn: func(_ context.Context, spec host.SpawnSpec) (host.Process, error) {
			spawned = &spec
			return proc, nil
		},
	})
	res, err := m.Invoke(t.Context(), "browser_exec", json.RawMessage(`{"script":"print(1)"}`), host.Env{CallID: "c1"})
	if err != nil || res.Content != "clicked #login" || res.UntrustedSource != "browser:browser-use" {
		t.Fatalf("exec = %q, %v", res.Content, err)
	}
	if spawned == nil || spawned.Path != cfg.UseBin {
		t.Fatalf("spawn = %+v", spawned)
	}
	if got := proc.in.buf.String(); got != "print(1)" {
		t.Fatalf("stdin = %q", got)
	}
	reqs := appr.requests()
	if len(reqs) != 1 || strings.Contains(reqs[0].Preview, "print(1)") {
		t.Fatalf("preview leaked the script: %+v", reqs)
	}
	if !strings.Contains(reqs[0].Preview, "browser-use") {
		t.Fatalf("preview does not name the backend: %q", reqs[0].Preview)
	}
}

// execReplacesTools proves browser-use replaces every page tool.
func execReplacesTools(t *testing.T) {
	t.Helper()
	cfg := execConfig(t)
	m := newModule(t.Context(), t, &cfg, Deps{Spawn: func(context.Context, host.SpawnSpec) (host.Process, error) {
		return &fakeProcess{done: make(chan struct{})}, nil
	}})
	for _, tool := range []string{"browser_navigate", "browser_screenshot", "browser_cdp", "browser_dialog"} {
		_, err := m.Invoke(t.Context(), tool, json.RawMessage(`{}`), host.Env{})
		var he *host.Error
		if !errors.As(err, &he) || he.Code != "module_unavailable" {
			t.Fatalf("%s: err = %v, want module_unavailable", tool, err)
		}
	}
}

// execNoCLI proves exec fails closed without the CLI or the spawner.
func execNoCLI(t *testing.T) {
	t.Helper()
	cfg := execConfig(t)
	cfg.UseBin = ""
	m := newModule(t.Context(), t, &cfg, Deps{})
	_, err := m.Invoke(t.Context(), "browser_exec", json.RawMessage(`{"script":"print(1)"}`), host.Env{})
	var he *host.Error
	if !errors.As(err, &he) || he.Code != "module_unavailable" {
		t.Fatalf("err = %v, want module_unavailable", err)
	}
}

// execDeniedApproval proves a denied exec never spawns the CLI.
func execDeniedApproval(t *testing.T) {
	t.Helper()
	cfg := execConfig(t)
	spawned := false
	m := newModule(t.Context(), t, &cfg, Deps{
		Approvals: &fakeApprovals{answer: host.ApprovalDeny},
		Spawn: func(context.Context, host.SpawnSpec) (host.Process, error) {
			spawned = true
			return &fakeProcess{done: make(chan struct{})}, nil
		},
	})
	if _, err := m.Invoke(t.Context(), "browser_exec", json.RawMessage(`{"script":"print(1)"}`), host.Env{}); err == nil {
		t.Fatal("unapproved exec ran")
	}
	if spawned {
		t.Fatal("denied exec spawned the CLI")
	}
}

// execOnCDPRefused proves exec is unavailable on the CDP engine and asks for
// no approval there.
func execOnCDPRefused(t *testing.T) {
	t.Helper()
	f := newFake()
	appr := &fakeApprovals{answer: host.ApprovalAllowOnce}
	m := newModule(t.Context(), t, new(moduleConfig()), Deps{Dial: dialTo(f), Approvals: appr})
	_, err := m.Invoke(t.Context(), "browser_exec", json.RawMessage(`{"script":"print(1)"}`), host.Env{})
	var he *host.Error
	if !errors.As(err, &he) || he.Code != "module_unavailable" {
		t.Fatalf("err = %v, want module_unavailable", err)
	}
	if len(appr.requests()) != 0 {
		t.Fatalf("exec asked for approval on CDP: %+v", appr.requests())
	}
}

// execOversizeOutput proves output beyond maxExecOutput is rejected.
func execOversizeOutput(t *testing.T) {
	t.Helper()
	cfg := execConfig(t)
	proc := &fakeProcess{out: bytes.Repeat([]byte("x"), maxExecOutput+1), done: make(chan struct{})}
	m := newModule(t.Context(), t, &cfg, Deps{
		Approvals: &fakeApprovals{answer: host.ApprovalAllowOnce},
		Spawn: func(context.Context, host.SpawnSpec) (host.Process, error) {
			return proc, nil
		},
	})
	_, err := m.Invoke(t.Context(), "browser_exec", json.RawMessage(`{"script":"print(1)"}`), host.Env{})
	var he *host.Error
	if !errors.As(err, &he) || he.Code != "too_large" {
		t.Fatalf("err = %v, want too_large", err)
	}
}

// fakeSecrets serves only the inherited names, like the host seam.
type fakeSecrets struct {
	env map[string]string
	err error
}

func (f fakeSecrets) ChildEnv(policy host.AllowPolicy) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([]string, 0, len(policy.Inherit))
	for _, name := range policy.Inherit {
		if value, ok := f.env[name]; ok {
			out = append(out, name+"="+value)
		}
	}
	return out, nil
}

// vault test credentials: neither may ever appear in a result, preview or error.
const (
	vaultUserValue = "hunter2-user"
	vaultPassValue = "s3cret-pass"
	vaultLoginArgs = `{"user_selector":"#user","password_selector":"#pass"}`
)

// TestF6TOOL015BrowserVault (TOOL-015): credentials come only from the secret
// seam, never appear in a result, preview or error, and every call needs an
// allow-once approval on an explicit local profile.
func TestF6TOOL015BrowserVault(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		run  func(*testing.T)
	}{
		{"fills both fields without printing values", vaultFillsFields},
		{"submit is opt-in and uses the form of the password field", vaultSubmits},
		{"negative control: real profile is opt-in", vaultNeedsProfile},
		{"negative control: missing vault config", vaultNeedsConfig},
		{"negative control: denied approval fills nothing", vaultDenied},
		{"negative control: missing field fails before typing", vaultMissingField},
		{"negative control: no secret seam", vaultNoSeam},
		{"negative control: half a vault config is refused at load", vaultHalfConfig},
	}
	for _, tc := range cases {
		t.Run(tc.name, tc.run)
	}
}

// vaultConfig returns a config that names both credential variables.
func vaultConfig(t *testing.T) ModuleConfig {
	t.Helper()
	cfg := moduleConfig()
	cfg.VaultUserEnv = "VAULT_USER"
	cfg.VaultPassEnv = "VAULT_PASS"
	return cfg
}

// vaultSecretsFake serves only the two configured vault variables.
func vaultSecretsFake() fakeSecrets {
	return fakeSecrets{env: map[string]string{"VAULT_USER": vaultUserValue, "VAULT_PASS": vaultPassValue}}
}

// vaultRecorder records the text typed and the page expressions evaluated.
type vaultRecorder struct {
	mu    sync.Mutex
	typed []string
	evals []string
}

// hook returns the transport callback that fills the recorder.
func (r *vaultRecorder) hook(t *testing.T) func(string, any) {
	return func(method string, params any) {
		switch method {
		case "Input.insertText":
			r.recordInsert(t, params)
		case "Runtime.evaluate":
			r.recordEval(t, params)
		}
	}
}

func (r *vaultRecorder) recordInsert(t *testing.T, params any) {
	inserted, ok := params.(map[string]string)
	if !ok {
		t.Errorf("insertText params = %T", params)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.typed = append(r.typed, inserted["text"])
}

func (r *vaultRecorder) recordEval(t *testing.T, params any) {
	evaluated, ok := params.(map[string]any)
	if !ok {
		t.Errorf("evaluate params = %T", params)
		return
	}
	expression, ok := evaluated["expression"].(string)
	if !ok {
		t.Errorf("evaluate expression = %T", evaluated["expression"])
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evals = append(r.evals, expression)
}

// vaultFillsFields proves both credentials reach the fields and neither the
// result, the meta map, the preview nor a page expression.
func vaultFillsFields(t *testing.T) {
	t.Helper()
	f := newFake()
	rec := &vaultRecorder{}
	f.onCall = rec.hook(t)
	f.results["Runtime.evaluate"] = json.RawMessage(`{"result":{"value":true}}`)
	appr := &fakeApprovals{answer: host.ApprovalAllowOnce}
	cfg := vaultConfig(t)
	m := newModule(t.Context(), t, &cfg, Deps{Dial: dialTo(f), Approvals: appr, Secrets: vaultSecretsFake()})
	res, err := m.Invoke(t.Context(), "browser_vault", json.RawMessage(vaultLoginArgs), host.Env{CallID: "c1"})
	if err != nil {
		t.Fatalf("vault: %v", err)
	}
	if res.UntrustedSource != "browser:page" || !strings.Contains(res.Content, "submitted=false") {
		t.Fatalf("result = %+v", res)
	}
	if !slices.Equal(rec.typed, []string{vaultUserValue, vaultPassValue}) {
		t.Fatalf("typed = %v, want the two credentials", rec.typed)
	}
	for _, leaked := range []string{res.Content, fmt.Sprint(res.Meta)} {
		if strings.Contains(leaked, vaultUserValue) || strings.Contains(leaked, vaultPassValue) {
			t.Fatalf("result leaked a credential: %q", leaked)
		}
	}
	reqs := appr.requests()
	if len(reqs) != 1 || reqs[0].Effect != host.EffectNonIdempotent {
		t.Fatalf("approvals = %+v", reqs)
	}
	if strings.Contains(reqs[0].Preview, vaultUserValue) || strings.Contains(reqs[0].Preview, vaultPassValue) {
		t.Fatalf("preview leaked a credential: %q", reqs[0].Preview)
	}
	for _, expression := range rec.evals {
		if strings.Contains(expression, vaultUserValue) || strings.Contains(expression, vaultPassValue) {
			t.Fatalf("credential entered a page expression: %q", expression)
		}
	}
}

// vaultSubmits proves submit is opt-in and uses the password field's form.
func vaultSubmits(t *testing.T) {
	t.Helper()
	f := newFake()
	rec := &vaultRecorder{}
	f.onCall = rec.hook(t)
	f.results["Runtime.evaluate"] = json.RawMessage(`{"result":{"value":true}}`)
	cfg := vaultConfig(t)
	m := newModule(t.Context(), t, &cfg, Deps{
		Dial: dialTo(f), Approvals: &fakeApprovals{answer: host.ApprovalAllowOnce}, Secrets: vaultSecretsFake(),
	})
	if _, err := m.Invoke(t.Context(), "browser_vault", json.RawMessage(`{"user_selector":"#user","password_selector":"#pass","submit":true}`), host.Env{}); err != nil {
		t.Fatalf("vault: %v", err)
	}
	submitted := false
	for _, expression := range rec.evals {
		if strings.Contains(expression, "requestSubmit") {
			submitted = true
		}
		if strings.Contains(expression, vaultPassValue) {
			t.Fatalf("credential entered a page expression: %q", expression)
		}
	}
	if !submitted {
		t.Fatalf("no submit expression in %v", rec.evals)
	}
}

// vaultNeedsProfile proves browser_vault is denied without local_profile.
func vaultNeedsProfile(t *testing.T) {
	t.Helper()
	f := newFake()
	cfg := vaultConfig(t)
	cfg.LocalProfile = false
	appr := &fakeApprovals{answer: host.ApprovalAllowOnce}
	m := newModule(t.Context(), t, &cfg, Deps{Dial: dialTo(f), Approvals: appr, Secrets: vaultSecretsFake()})
	_, err := m.Invoke(t.Context(), "browser_vault", json.RawMessage(vaultLoginArgs), host.Env{})
	var he *host.Error
	if !errors.As(err, &he) || he.Code != "denied" {
		t.Fatalf("err = %v, want denied", err)
	}
	if len(f.calls) != 0 || len(appr.requests()) != 0 {
		t.Fatalf("calls = %v, approvals = %+v", f.calls, appr.requests())
	}
}

// vaultNeedsConfig proves browser_vault fails closed without the two names.
func vaultNeedsConfig(t *testing.T) {
	t.Helper()
	f := newFake()
	m := newModule(t.Context(), t, new(moduleConfig()), Deps{Dial: dialTo(f), Secrets: vaultSecretsFake()})
	_, err := m.Invoke(t.Context(), "browser_vault", json.RawMessage(vaultLoginArgs), host.Env{})
	var he *host.Error
	if !errors.As(err, &he) || he.Code != "module_unavailable" {
		t.Fatalf("err = %v, want module_unavailable", err)
	}
}

// vaultDenied proves a denied approval fills nothing.
func vaultDenied(t *testing.T) {
	t.Helper()
	f := newFake()
	f.results["Runtime.evaluate"] = json.RawMessage(`{"result":{"value":true}}`)
	cfg := vaultConfig(t)
	m := newModule(t.Context(), t, &cfg, Deps{
		Dial: dialTo(f), Approvals: &fakeApprovals{answer: host.ApprovalDeny}, Secrets: vaultSecretsFake(),
	})
	if _, err := m.Invoke(t.Context(), "browser_vault", json.RawMessage(vaultLoginArgs), host.Env{}); err == nil {
		t.Fatal("unapproved vault ran")
	}
	if len(f.calls) != 0 {
		t.Fatalf("denied vault called CDP: %v", f.calls)
	}
}

// vaultMissingField proves a missing field fails before any text is typed.
func vaultMissingField(t *testing.T) {
	t.Helper()
	f := newFake()
	f.results["Runtime.evaluate"] = json.RawMessage(`{"result":{"value":false}}`)
	cfg := vaultConfig(t)
	m := newModule(t.Context(), t, &cfg, Deps{
		Dial: dialTo(f), Approvals: &fakeApprovals{answer: host.ApprovalAllowOnce}, Secrets: vaultSecretsFake(),
	})
	_, err := m.Invoke(t.Context(), "browser_vault", json.RawMessage(vaultLoginArgs), host.Env{})
	var he *host.Error
	if !errors.As(err, &he) || he.Code != "invalid_args" {
		t.Fatalf("err = %v, want invalid_args", err)
	}
	if f.called("Input.insertText") {
		t.Fatalf("typed into a missing field: %v", f.calls)
	}
}

// vaultNoSeam proves a missing secret seam fails closed without leaking a value.
func vaultNoSeam(t *testing.T) {
	t.Helper()
	f := newFake()
	cfg := vaultConfig(t)
	m := newModule(t.Context(), t, &cfg, Deps{
		Dial: dialTo(f), Approvals: &fakeApprovals{answer: host.ApprovalAllowOnce},
	})
	_, err := m.Invoke(t.Context(), "browser_vault", json.RawMessage(vaultLoginArgs), host.Env{})
	var he *host.Error
	if !errors.As(err, &he) || he.Code != "module_unavailable" {
		t.Fatalf("err = %v, want module_unavailable", err)
	}
	if strings.Contains(err.Error(), vaultUserValue) {
		t.Fatalf("error leaked a credential: %v", err)
	}
}

// vaultHalfConfig proves half a vault config is refused at load.
func vaultHalfConfig(t *testing.T) {
	t.Helper()
	cfg := vaultConfig(t)
	cfg.VaultPassEnv = ""
	if _, err := New(&cfg, Deps{}); !errors.Is(err, errBadConfig) {
		t.Fatalf("New = %v, want errBadConfig", err)
	}
}
