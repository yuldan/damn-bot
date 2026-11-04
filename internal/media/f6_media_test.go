//go:build !minimal

package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/damnbot/damnbot/internal/modules/host"
)

type fakeCompleter struct {
	mu      sync.Mutex
	reqs    []host.Request
	content string
	err     error
}

func (f *fakeCompleter) Complete(_ context.Context, req host.Request) (host.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, req)
	return host.Response{Content: f.content, Usage: host.Usage{InputTokens: 7, OutputTokens: 3}}, f.err
}

func (f *fakeCompleter) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

func pngFixture(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 40, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func visionModule(t *testing.T, comp host.Completer, vision bool) *Module {
	t.Helper()
	return testModule(t, &Section{Enabled: true}, Deps{
		LLM: comp, Vision: vision, VisionModel: "fake/vision",
	}, newMemStore())
}

func TestF6TOOL085VisionAnalyze(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cat.png"), pngFixture(t, 12, 9), 0o600); err != nil {
		t.Fatal(err)
	}
	comp := &fakeCompleter{content: "a small test card"}
	m := visionModule(t, comp, true)
	res, err := m.Invoke(context.Background(), ToolVisionAnalyze,
		json.RawMessage(`{"path":"cat.png","prompt":"what is shown?"}`),
		host.Env{Cwd: dir})
	if err != nil {
		t.Fatalf("vision_analyze: %v", err)
	}
	if res.Content != "a small test card" || res.UntrustedSource != "media:vision" {
		t.Fatalf("result = %+v", res)
	}
	if res.Meta["kind"] != "image" || res.Meta["mime"] != "image/png" {
		t.Fatalf("meta = %v", res.Meta)
	}
	if comp.calls() != 1 {
		t.Fatalf("completer calls = %d, want 1", comp.calls())
	}
	req := comp.reqs[0]
	if req.Model != "fake/vision" || len(req.Messages) != 1 {
		t.Fatalf("request = %+v", req)
	}
	msg := req.Messages[0]
	if msg.Content != "what is shown?" || len(msg.Images) != 1 {
		t.Fatalf("message = %+v", msg)
	}
	img := msg.Images[0]
	if img.MIME != "image/png" || !img.Untrusted || len(img.Data) == 0 {
		t.Fatalf("image = %+v", img)
	}
}

// TestF6MediaVisionGate covers the EC-PROV-024 negative path: a text-only
// session never calls the model, and a path outside the workspace is never
// read.
func TestF6MediaVisionGate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cat.png"), pngFixture(t, 12, 9), 0o600); err != nil {
		t.Fatal(err)
	}
	// EC-PROV-024: a text-only session rejects the call before any provider
	// request and before reading the file.
	textOnly := &fakeCompleter{content: "must not run"}
	tm := visionModule(t, textOnly, false)
	_, err := tm.Invoke(context.Background(), ToolVisionAnalyze,
		json.RawMessage(`{"path":"cat.png"}`), host.Env{Cwd: dir})
	var he *host.Error
	if !errors.As(err, &he) || he.Code != "module_unavailable" {
		t.Fatalf("text-only gate = %v", err)
	}
	if textOnly.calls() != 0 {
		t.Fatalf("text-only completer calls = %d, want 0", textOnly.calls())
	}

	// A path outside the workspace is never read, and the error carries no
	// media bytes.
	outside := filepath.Join(t.TempDir(), "secret.png")
	if werr := os.WriteFile(outside, pngFixture(t, 4, 4), 0o600); werr != nil {
		t.Fatal(werr)
	}
	esc := &fakeCompleter{content: "must not run"}
	em := visionModule(t, esc, true)
	_, err = em.Invoke(context.Background(), ToolVisionAnalyze,
		json.RawMessage(`{"path":"../`+filepath.Base(outside)+`"}`), host.Env{Cwd: dir})
	if !errors.As(err, &he) || he.Code != "invalid_args" {
		t.Fatalf("escape = %v", err)
	}
	if esc.calls() != 0 || strings.Contains(err.Error(), "iVBOR") {
		t.Fatalf("escape leaked data or called provider: %v calls=%d", err, esc.calls())
	}
}

func TestF6MEDIA012VisionAnalyze(t *testing.T) {
	t.Parallel()
	data := pngFixture(t, 3000, 2000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		if _, err := w.Write(data); err != nil {
			t.Errorf("write fixture: %v", err)
		}
	}))
	defer srv.Close()
	comp := &fakeCompleter{content: "resized image"}
	m := visionModule(t, comp, true)
	res, err := m.Invoke(context.Background(), ToolVisionAnalyze,
		json.RawMessage(`{"url":"`+srv.URL+`/photo.png"}`), host.Env{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("vision URL: %v", err)
	}
	if res.Meta["resized"] != true {
		t.Fatalf("meta = %v, want a resized image", res.Meta)
	}
	img := comp.reqs[0].Messages[0].Images[0]
	if img.Width > 2048 || img.Height > 2048 {
		t.Fatalf("preflight dims = %dx%d", img.Width, img.Height)
	}
	if img.Name != "photo.png" || !img.Untrusted {
		t.Fatalf("image name/taint = %+v", img)
	}
	// The response text is untrusted, not the local path or URL.
	if res.UntrustedSource != "media:vision" || strings.Contains(res.Content, srv.URL) {
		t.Fatalf("taint = %+v", res)
	}
}
