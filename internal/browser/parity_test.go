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
	"net/url"
	"strings"
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
