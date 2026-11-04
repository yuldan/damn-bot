//go:build !minimal

package provider_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider"
	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// TestF6MEDIA013ImageRouting: every image up to MaxImages is sent natively
// without a size rejection; one over the route limit fails before any request.
func TestF6MEDIA013ImageRouting(t *testing.T) {
	t.Parallel()
	srv := providertest.New(t, nil, providertest.Text("ok"))
	vr := route("v", srv.URL)
	vr.Vision = true
	o := client(t, &provider.Config{Routes: []provider.Route{vr}})

	req := ask("fake/v")
	for range 5 {
		req.Messages[0].Images = append(req.Messages[0].Images,
			provider.Image{Name: "fixture.png", Data: pngImage(t, 32, 24)})
	}
	if _, err := o.Do(t.Context(), provider.PurposeTurn, req, nil); err != nil {
		t.Fatalf("multi-image: %v", err)
	}
	body := string(srv.Requests()[0])
	if got := strings.Count(body, `"type":"image_url"`); got != 5 {
		t.Fatalf("image parts = %d, want 5", got)
	}

	before := len(srv.Requests())
	over := ask("fake/v")
	for range provider.MaxImages + 1 {
		over.Messages[0].Images = append(over.Messages[0].Images,
			provider.Image{Name: "fixture.png", Data: pngImage(t, 8, 8)})
	}
	if _, err := o.Do(t.Context(), provider.PurposeTurn, over, nil); !errors.Is(err, provider.ErrImage) {
		t.Fatalf("over-limit images = %v", err)
	}
	if len(srv.Requests()) != before {
		t.Fatal("over-limit image reached the route")
	}
}
