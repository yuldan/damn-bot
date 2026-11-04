package e2e

import (
	"bytes"
	"encoding/base64"
	"fmt"
	stdimage "image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// TestF6SLASH099ImageE2E drives the real binary: an ACP image resource is
// spilled to a blob, survives compaction, and reaches the provider request as
// a data: URL. The request body is the security boundary: no other channel may
// carry the bytes.
func TestF6SLASH099ImageE2E(t *testing.T) {
	e := New(t, providertest.Text("saw the image"))
	t.Setenv("KANTOR_KEY", "sk-e2e")
	cfg := fmt.Sprintf(customProviderConfig, e.Server.URL)
	cfg = strings.Replace(cfg, `capabilities = ["tools"]`, `capabilities = ["tools", "vision"]`, 1)
	p := startACP(t, e, cfg)
	p.initSession(e.Workdir)

	img := stdimage.NewNRGBA(stdimage.Rect(0, 0, 8, 8))
	for y := range 8 {
		for x := range 8 {
			img.Set(x, y, color.NRGBA{R: 200, G: 30, B: 30, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	blob := base64.StdEncoding.EncodeToString(buf.Bytes())
	p.send(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "session/prompt", "params": map[string]any{
		"sessionId": p.sid,
		"prompt": []any{
			map[string]any{"type": "text", "text": "what is this"},
			map[string]any{"type": "resource", "resource": map[string]any{
				"uri": "file:///shot.png", "mimeType": "image/png", "blob": blob,
			}},
		},
	}})
	var done map[string]any
	for done = p.recv(); done["id"] != float64(3); done = p.recv() {
		if done["error"] != nil {
			t.Fatalf("unexpected error frame: %v", done)
		}
	}
	if done["error"] != nil {
		t.Fatalf("session/prompt = %v (stderr %s)", done, p.errb.String())
	}
	bodies := e.Server.Requests()
	if len(bodies) == 0 {
		t.Fatal("provider received no request")
	}
	body := string(bodies[len(bodies)-1])
	if !strings.Contains(body, `"image_url"`) || !strings.Contains(body, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(buf.Bytes())) {
		t.Fatalf("image did not reach the provider body: %s", body)
	}
}
