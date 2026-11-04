//go:build !minimal

package provider_test

import (
	"encoding/binary"
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

func mp4Video(t testing.TB, durationMS uint32) []byte {
	t.Helper()
	ftyp := make([]byte, 24)
	binary.BigEndian.PutUint32(ftyp[0:4], 24)
	copy(ftyp[4:8], "ftyp")
	copy(ftyp[8:12], "isom")
	binary.BigEndian.PutUint32(ftyp[12:16], 0x200)
	copy(ftyp[16:20], "isom")
	copy(ftyp[20:24], "iso2")
	mvhd := make([]byte, 28)
	binary.BigEndian.PutUint32(mvhd[0:4], 28)
	copy(mvhd[4:8], "mvhd")
	binary.BigEndian.PutUint32(mvhd[20:24], 1000)
	binary.BigEndian.PutUint32(mvhd[24:28], durationMS)
	moov := make([]byte, 8, 8+len(mvhd))
	binary.BigEndian.PutUint32(moov[0:4], 36)
	copy(moov[4:8], "moov")
	return append(append(ftyp, moov...), mvhd...)
}

// TestF6MediaVideoProviderGate: a probed video is encoded only on a vision
// route and an over-duration video fails before the request.
func TestF6MediaVideoProviderGate(t *testing.T) {
	t.Parallel()
	srv := providertest.New(t, nil, providertest.Text("ok"))
	vr := route("v", srv.URL)
	vr.Vision = true
	o := client(t, &provider.Config{Routes: []provider.Route{vr}})

	req := ask("fake/v")
	req.Messages[0].Videos = []provider.Video{{Name: "clip.mp4", Data: mp4Video(t, 5000)}}
	if _, err := o.Do(t.Context(), provider.PurposeTurn, req, nil); err != nil {
		t.Fatalf("video: %v", err)
	}
	body := string(srv.Requests()[0])
	if !strings.Contains(body, `"video_url"`) || !strings.Contains(body, "data:video/mp4;base64,") {
		t.Fatalf("video body = %.400s", body)
	}

	before := len(srv.Requests())
	tooLong := ask("fake/v")
	tooLong.Messages[0].Videos = []provider.Video{{Name: "long.mp4", Data: mp4Video(t, 121000)}}
	if _, err := o.Do(t.Context(), provider.PurposeTurn, tooLong, nil); !errors.Is(err, provider.ErrVideo) {
		t.Fatalf("over-duration video = %v", err)
	}
	if len(srv.Requests()) != before {
		t.Fatal("over-duration video reached the route")
	}

	// A text-only route still refuses the video before the request.
	text := providertest.New(t, nil, providertest.Text("no"))
	plain := client(t, &provider.Config{Routes: []provider.Route{route("m", text.URL)}})
	control := ask("fake/m")
	control.Messages[0].Videos = []provider.Video{{Name: "clip.mp4", Data: mp4Video(t, 2000)}}
	if _, err := plain.Do(t.Context(), provider.PurposeTurn, control, nil); !errors.Is(err, provider.ErrNoVision) {
		t.Fatalf("text-only video = %v", err)
	}
	if len(text.Requests()) != 0 {
		t.Fatal("text-only route received a video")
	}
}
