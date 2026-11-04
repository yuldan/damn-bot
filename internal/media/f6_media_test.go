//go:build !minimal

package media

import (
	"bytes"
	"context"
	"encoding/binary"
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

	"github.com/damnbot/damnbot/internal/media/video"
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

// pathArgs marshals a path safely: Windows paths contain backslashes, which
// are invalid inside a hand-built JSON string.
func pathArgs(t *testing.T, path string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"path": path})
	if err != nil {
		t.Fatal(err)
	}
	return raw
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

// mp4Fixture writes a minimal ISO-BMFF file whose mvhd reports durationMS.
func mp4Fixture(t *testing.T, durationMS uint32) []byte {
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
	binary.BigEndian.PutUint32(mvhd[20:24], 1000) // timescale
	binary.BigEndian.PutUint32(mvhd[24:28], durationMS)
	moov := make([]byte, 8, 8+len(mvhd))
	binary.BigEndian.PutUint32(moov[0:4], 36)
	copy(moov[4:8], "moov")
	moov = append(moov, mvhd...)
	return append(ftyp, moov...)
}

func TestF6TOOL081VideoAnalyze(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "clip.mp4"), mp4Fixture(t, 5000), 0o600); err != nil {
		t.Fatal(err)
	}
	comp := &fakeCompleter{content: "a person waves"}
	m := visionModule(t, comp, true)
	res, err := m.Invoke(context.Background(), ToolVideoAnalyze,
		json.RawMessage(`{"path":"clip.mp4","prompt":"what happens?"}`), host.Env{Cwd: dir})
	if err != nil {
		t.Fatalf("video_analyze: %v", err)
	}
	if res.Content != "a person waves" || res.UntrustedSource != "media:video" {
		t.Fatalf("result = %+v", res)
	}
	if res.Meta["mime"] != "video/mp4" || res.Meta["duration_s"] != 5.0 {
		t.Fatalf("meta = %v", res.Meta)
	}
	req := comp.reqs[0]
	if len(req.Messages) != 1 || len(req.Messages[0].Videos) != 1 || len(req.Messages[0].Images) != 0 {
		t.Fatalf("request = %+v", req)
	}
	vid := req.Messages[0].Videos[0]
	if vid.MIME != "video/mp4" || vid.DurationS != 5 || !vid.Untrusted || len(vid.Data) == 0 {
		t.Fatalf("video = %+v", vid)
	}
}

func TestF6MEDIA014VideoAnalyze(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cases := []struct {
		name string
		data []byte
		code string
	}{
		{"duration", mp4Fixture(t, 121000), "too_large"},
		{"unsupported", []byte("not a video at all"), "invalid_args"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(dir, tc.name+".bin")
			if err := os.WriteFile(file, tc.data, 0o600); err != nil {
				t.Fatal(err)
			}
			comp := &fakeCompleter{content: "must not run"}
			m := visionModule(t, comp, true)
			_, err := m.Invoke(context.Background(), ToolVideoAnalyze,
				pathArgs(t, filepath.Base(file)), host.Env{Cwd: dir})
			var he *host.Error
			if !errors.As(err, &he) || he.Code != tc.code {
				t.Fatalf("%s = %v, want %s", tc.name, err, tc.code)
			}
			if comp.calls() != 0 {
				t.Fatalf("%s reached the provider", tc.name)
			}
		})
	}

	// Size is rejected from Stat before the file is read.
	big := filepath.Join(dir, "big.mp4")
	if err := os.WriteFile(big, mp4Fixture(t, 1000), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(big, video.MaxBytes+1); err != nil {
		t.Fatal(err)
	}
	comp := &fakeCompleter{content: "must not run"}
	m := visionModule(t, comp, true)
	_, err := m.Invoke(context.Background(), ToolVideoAnalyze,
		json.RawMessage(`{"path":"big.mp4"}`), host.Env{Cwd: dir})
	var he *host.Error
	if !errors.As(err, &he) || he.Code != "too_large" || comp.calls() != 0 {
		t.Fatalf("size bound = %v calls=%d", err, comp.calls())
	}
}

// TestF6MediaVideoApproval: a local video outside the workspace is a non-read
// action and needs an explicit allow-once approval before the bytes are read.
func TestF6MediaVideoApproval(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.mp4")
	sensitive := append(mp4Fixture(t, 2000), []byte("SENTINEL-VIDEO-BODY")...)
	if err := os.WriteFile(outside, sensitive, 0o600); err != nil {
		t.Fatal(err)
	}
	approvals := &fakeApprovals{answer: host.ApprovalDeny}
	comp := &fakeCompleter{content: "described"}
	m := testModule(t, &Section{Enabled: true}, Deps{
		Approvals: approvals, LLM: comp, Vision: true, VisionModel: "fake/vision",
	}, newMemStore())
	_, err := m.Invoke(context.Background(), ToolVideoAnalyze,
		pathArgs(t, outside), host.Env{Cwd: dir})
	var he *host.Error
	if !errors.As(err, &he) || he.Code != "denied" {
		t.Fatalf("outside without approval = %v", err)
	}
	if comp.calls() != 0 || len(approvals.reqs) != 1 {
		t.Fatalf("calls=%d approvals=%d", comp.calls(), len(approvals.reqs))
	}
	req := approvals.reqs[0]
	if req.Effect != host.EffectNonIdempotent || strings.Contains(req.Preview, "SENTINEL") {
		t.Fatalf("approval = %+v", req)
	}
	approvals.answer = host.ApprovalAllowOnce
	if _, err := m.Invoke(context.Background(), ToolVideoAnalyze,
		pathArgs(t, outside), host.Env{Cwd: dir}); err != nil {
		t.Fatalf("approved outside video: %v", err)
	}
	if comp.calls() != 1 {
		t.Fatalf("approved calls = %d, want 1", comp.calls())
	}
}

func TestF6MediaVideoProbeBounds(t *testing.T) {
	t.Parallel()
	info, err := video.Probe(mp4Fixture(t, 3500))
	if err != nil || info.DurationS != 3.5 || info.MIME != "video/mp4" {
		t.Fatalf("probe = %+v, %v", info, err)
	}
	for _, data := range [][]byte{nil, []byte("garbage"), mp4Fixture(t, 1000)[:10]} {
		if _, err := video.Probe(data); err == nil {
			t.Fatalf("probe(%d bytes) succeeded", len(data))
		}
	}
	if err := video.Check(video.Info{DurationS: video.MaxDuration + 1}); err == nil {
		t.Fatal("duration over the limit passed")
	}
}
