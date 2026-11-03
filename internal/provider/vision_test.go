//go:build !minimal

package provider_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider"
	"github.com/damnbot/damnbot/internal/provider/providertest"
)

func pngImage(t testing.TB, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = byte(i)
	}
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 255
	}
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func jpegImage(t testing.TB, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = byte(i * 7)
	}
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 255
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 70}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// ecVisionGate (EC-PROV-024): a text-only route never receives an image and
// the failure happens before any request; a text prompt still works.
func ecVisionGate(t *testing.T) {
	srv := providertest.New(t, nil, providertest.Text("ok"))
	o := client(t, &provider.Config{Routes: []provider.Route{route("m", srv.URL)}})
	req := ask("fake/m")
	req.Messages[0].Images = []provider.Image{{Name: "cat.png", Data: pngImage(t, 8, 8)}}
	if _, err := o.Do(t.Context(), provider.PurposeTurn, req, nil); !errors.Is(err, provider.ErrNoVision) {
		t.Fatalf("text-only image = %v", err)
	}
	if got := len(srv.Requests()); got != 0 {
		t.Fatalf("text-only route got %d requests", got)
	}
	if _, err := o.Do(t.Context(), provider.PurposeTurn, ask("fake/m"), nil); err != nil {
		t.Fatalf("text control = %v", err)
	}
	if len(srv.Requests()) != 1 {
		t.Fatal("text control did not reach the route")
	}
}

func TestVisionRoutingAndPreflight(t *testing.T) {
	t.Parallel()
	primary := providertest.New(t, nil, providertest.Text("wrong"))
	vision := providertest.New(t, nil, providertest.Text("ok"))
	vr := route("v", vision.URL)
	vr.Vision = true
	o := client(t, &provider.Config{
		Routes:   []provider.Route{route("m", primary.URL), vr},
		Fallback: []string{"fake/v"},
	})
	req := ask("fake/m")
	req.Messages[0].Images = []provider.Image{{Name: "big.png", Data: pngImage(t, 3000, 2000)}}
	resp, err := o.Do(t.Context(), provider.PurposeTurn, req, nil)
	if err != nil || resp.Content != "ok" || resp.FallbackFrom != "fake/m" {
		t.Fatalf("vision fallback = %+v, %v", resp, err)
	}
	if len(primary.Requests()) != 0 || len(vision.Requests()) != 1 {
		t.Fatalf("requests primary=%d vision=%d", len(primary.Requests()), len(vision.Requests()))
	}
	body := string(vision.Requests()[0])
	if !strings.Contains(body, `"image_url"`) || !strings.Contains(body, "data:image/png;base64,") {
		t.Fatalf("vision body = %s", body)
	}
	raw := visionImageBytes(t, body)
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width > 2048 || cfg.Height > 2048 {
		t.Fatalf("preflighted dims = %dx%d, %v", cfg.Width, cfg.Height, err)
	}
}

func visionImageBytes(t *testing.T, body string) []byte {
	t.Helper()
	var payload struct {
		Messages []struct {
			Content any `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil || len(payload.Messages) == 0 {
		t.Fatalf("body decode: %v", err)
	}
	parts, ok := payload.Messages[0].Content.([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("content parts = %#v", payload.Messages[0].Content)
	}
	part, ok := parts[1].(map[string]any)
	if !ok {
		t.Fatalf("image part = %#v", parts[1])
	}
	iu, ok := part["image_url"].(map[string]any)
	if !ok {
		t.Fatalf("image_url = %#v", part["image_url"])
	}
	url, ok := iu["url"].(string)
	if !ok {
		t.Fatalf("url = %#v", iu["url"])
	}
	raw, err := base64.StdEncoding.DecodeString(url[strings.Index(url, ",")+1:])
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestVisionPreflightFailure(t *testing.T) {
	t.Parallel()
	srv := providertest.New(t, nil, providertest.Text("ok"))
	vr := route("v", srv.URL)
	vr.Vision = true
	o := client(t, &provider.Config{Routes: []provider.Route{vr}})
	req := ask("fake/v")
	req.Messages[0].Images = []provider.Image{{Name: "bad.jpg", Data: []byte{0xFF, 0xD8, 0xFF, 0x00}}}
	if _, err := o.Do(t.Context(), provider.PurposeTurn, req, nil); !errors.Is(err, provider.ErrImage) {
		t.Fatalf("bad image = %v", err)
	}
	if len(srv.Requests()) != 0 {
		t.Fatal("bad image reached the route")
	}
}

func TestVisionPrepareSuite50(t *testing.T) {
	t.Parallel()
	for i := range 50 {
		w, h := 16+(i*37)%1600, 16+(i*53)%1200
		data := pngImage(t, w, h)
		if i%2 == 1 {
			data = jpegImage(t, w, h)
		}
		out, err := provider.PrepareImages([]provider.Image{{Name: "fixture", Data: data, Untrusted: i%3 == 0}})
		if err != nil || len(out) != 1 || out[0].Width > 2048 || out[0].Height > 2048 {
			t.Fatalf("fixture %d: %+v, %v", i, out, err)
		}
		if out[0].MIME != "image/png" && out[0].MIME != "image/jpeg" {
			t.Fatalf("fixture %d mime = %q", i, out[0].MIME)
		}
		if out[0].Untrusted != (i%3 == 0) {
			t.Fatalf("fixture %d lost the untrusted flag", i)
		}
	}
}
