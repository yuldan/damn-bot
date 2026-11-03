package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/browser/cdp"
	"github.com/damnbot/damnbot/internal/modules/host"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

type fakeTransport struct {
	mu      sync.Mutex
	calls   []string
	onCall  func(method string, params any)
	results map[string]json.RawMessage
	events  chan cdp.Event
	done    chan struct{}
	once    sync.Once
}

func newFake() *fakeTransport {
	return &fakeTransport{
		results: map[string]json.RawMessage{},
		events:  make(chan cdp.Event, 16),
		done:    make(chan struct{}),
	}
}

func (f *fakeTransport) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	f.mu.Lock()
	f.calls = append(f.calls, method)
	hook := f.onCall
	res, ok := f.results[method]
	f.mu.Unlock()
	if hook != nil {
		hook(method, params)
	}
	if ok {
		return res, nil
	}
	return json.RawMessage(`{}`), nil
}

func (f *fakeTransport) Events() <-chan cdp.Event { return f.events }
func (f *fakeTransport) Done() <-chan struct{}    { return f.done }
func (f *fakeTransport) Close() error {
	f.once.Do(func() {
		close(f.done)
		close(f.events)
	})
	return nil
}

func (f *fakeTransport) called(method string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.calls, method)
}

func emit(f *fakeTransport, method, params string) {
	f.events <- cdp.Event{Method: method, Params: json.RawMessage(params)}
}

func mustPage(t *testing.T, f Transport, cfg Config) *Page {
	t.Helper()
	p, err := NewCDPPage(f, cfg)
	if err != nil {
		t.Fatalf("NewPage: %v", err)
	}
	t.Cleanup(func() {
		if err := p.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return p
}

func navEvents(url string) func(*fakeTransport) {
	return func(f *fakeTransport) {
		f.onCall = func(method string, _ any) {
			if method != "Page.navigate" {
				return
			}
			emit(f, "Page.frameNavigated", `{"frame":{"id":"F","url":"`+url+`"}}`)
			emit(f, "Page.loadEventFired", `{}`)
		}
	}
}

func TestCDPAllowlist(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	allow := []string{"https://app.example"}

	t.Run("allowed origin", func(t *testing.T) {
		f := newFake()
		navEvents("https://app.example/dashboard")(f)
		p := mustPage(t, f, Config{AllowOrigins: allow})
		final, err := p.Navigate(ctx, "https://app.example/")
		if err != nil || final != "https://app.example/" {
			t.Fatalf("Navigate = %q, %v", final, err)
		}
	})

	t.Run("foreign origin denied before dial", func(t *testing.T) {
		f := newFake()
		p := mustPage(t, f, Config{AllowOrigins: allow})
		if _, err := p.Navigate(ctx, "https://evil.example/"); !errors.Is(err, errBlockedOrigin) {
			t.Fatalf("err = %v, want errBlockedOrigin", err)
		}
		if len(f.calls) != 0 {
			t.Fatalf("CDP calls = %v, want none", f.calls)
		}
	})

	t.Run("redirect to foreign origin denied", func(t *testing.T) {
		f := newFake()
		navEvents("https://evil.example/steal")(f)
		p := mustPage(t, f, Config{AllowOrigins: allow})
		if _, err := p.Navigate(ctx, "https://app.example/go"); !errors.Is(err, errBlockedOrigin) {
			t.Fatalf("err = %v, want errBlockedOrigin", err)
		}
		if !f.called("Page.stopLoading") {
			t.Fatalf("load was not stopped: %v", f.calls)
		}
	})

	t.Run("engine without allowlist is unavailable", func(t *testing.T) {
		var m *Page
		_, err := m.Invoke(ctx, "browser_navigate", json.RawMessage(`{"url":"https://app.example/"}`), host.Env{})
		var he *host.Error
		if !errors.As(err, &he) || he.Code != "module_unavailable" {
			t.Fatalf("err = %v, want module_unavailable", err)
		}
	})
}

func TestNavigateURLPolicy(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"http://127.0.0.1/", "http://[::1]/", "http://localhost/", "http://10.0.0.1/", "ftp://app.example/", "https://user@app.example/"} {
		f := newFake()
		p := mustPage(t, f, Config{AllowOrigins: []string{"https://app.example", "http://127.0.0.1"}})
		if _, err := p.Navigate(context.Background(), raw); err == nil {
			t.Fatalf("Navigate(%q) succeeded", raw)
		}
	}
	f := newFake()
	navEvents("http://127.0.0.1:9222/")(f)
	p := mustPage(t, f, Config{AllowOrigins: []string{"http://127.0.0.1:9222"}, LocalProfile: true})
	if _, err := p.Navigate(context.Background(), "http://127.0.0.1:9222/"); err != nil {
		t.Fatalf("local profile Navigate: %v", err)
	}
}

func TestEvalBounds(t *testing.T) {
	t.Parallel()
	f := newFake()
	f.results["Runtime.evaluate"] = json.RawMessage(`{"result":{"type":"string","value":"0123456789"}}`)
	p := mustPage(t, f, Config{AllowOrigins: []string{"https://app.example"}, MaxResult: 4})
	if _, err := p.Eval(context.Background(), "1"); !errors.Is(err, errTooLarge) {
		t.Fatalf("err = %v, want errTooLarge", err)
	}
	f.results["Runtime.evaluate"] = json.RawMessage(`{"result":{"type":"number","value":7}}`)
	got, err := p.Eval(context.Background(), "1")
	if err != nil || got != "7" {
		t.Fatalf("Eval = %q, %v", got, err)
	}
}

func TestScreenshotBounds(t *testing.T) {
	t.Parallel()
	f := newFake()
	big := base64.StdEncoding.EncodeToString(make([]byte, 64))
	f.results["Page.captureScreenshot"] = json.RawMessage(`{"data":"` + big + `"}`)
	p := mustPage(t, f, Config{AllowOrigins: []string{"https://app.example"}, MaxScreenshot: 8})
	if _, err := p.Screenshot(context.Background()); !errors.Is(err, errTooLarge) {
		t.Fatalf("err = %v, want errTooLarge", err)
	}
	small := base64.StdEncoding.EncodeToString([]byte("png"))
	f.results["Page.captureScreenshot"] = json.RawMessage(`{"data":"` + small + `"}`)
	got, err := p.Screenshot(context.Background())
	if err != nil || string(got) != "png" {
		t.Fatalf("Screenshot = %q, %v", got, err)
	}
}

func TestClickType(t *testing.T) {
	t.Parallel()
	f := newFake()
	p := mustPage(t, f, Config{AllowOrigins: []string{"https://app.example"}})
	if err := p.Click(context.Background(), 10, 20, true); err != nil {
		t.Fatalf("Click: %v", err)
	}
	if err := p.Type(context.Background(), "hello", true); err != nil {
		t.Fatalf("Type: %v", err)
	}
	if !f.called("Input.dispatchMouseEvent") || !f.called("Input.insertText") {
		t.Fatalf("calls = %v", f.calls)
	}
}

func TestWaitLoadTimeoutAndClose(t *testing.T) {
	t.Parallel()
	f := newFake()
	p := mustPage(t, f, Config{AllowOrigins: []string{"https://app.example"}, LoadTimeout: 30 * time.Millisecond})
	if err := p.WaitLoad(context.Background()); !errors.Is(err, errTimeout) {
		t.Fatalf("err = %v, want errTimeout", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := p.WaitLoad(context.Background()); !errors.Is(err, errClosed) {
		t.Fatalf("err = %v, want errClosed", err)
	}
}

func TestModuleApproval(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFake()
	p := mustPage(t, f, Config{AllowOrigins: []string{"https://app.example"}})
	m := p
	for _, tc := range []struct{ tool, args, secret string }{
		{"browser_click", `{"x":1,"y":2}`, ""},
		{"browser_type", `{"text":"hunter2"}`, "hunter2"},
	} {
		_, err := m.Invoke(ctx, tc.tool, json.RawMessage(tc.args), host.Env{})
		var he *host.Error
		if !errors.As(err, &he) || he.Code != "denied" {
			t.Fatalf("%s err = %v, want denied", tc.tool, err)
		}
		if tc.secret != "" && strings.Contains(he.Message, tc.secret) {
			t.Fatalf("preview leaked content: %q", he.Message)
		}
	}
	if f.called("Input.dispatchMouseEvent") || f.called("Input.insertText") {
		t.Fatal("unapproved input reached the page")
	}
	if _, err := m.Invoke(ctx, "browser_click", json.RawMessage(`{"x":1,"y":2}`), host.Env{Approved: true}); err != nil {
		t.Fatalf("approved click: %v", err)
	}
	if _, err := m.Invoke(ctx, "browser_type", json.RawMessage(`{"text":"ok"}`), host.Env{Approved: true}); err != nil {
		t.Fatalf("approved type: %v", err)
	}
}
