package gen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/modules/host"
)

const f6Crypt = "f6-fixture-value"

// imagePath is the vendor path each closed image backend must call.
func imagePath(name string) string {
	switch name {
	case "fal":
		return "/fal-ai/flux/dev"
	case "openrouter":
		return "/api/v1/chat/completions"
	case "krea":
		return "/api/v1/generate"
	}
	return "/v1/images/generations"
}

type f6Call struct {
	method string
	path   string
	header http.Header
	body   map[string]any
}

// f6Vendor records every request and answers POST/GET with fixed JSON.
type f6Vendor struct {
	t      *testing.T
	mu     sync.Mutex
	calls  []f6Call
	create string
	status string
}

func newF6Vendor(t *testing.T, create, status string) (*f6Vendor, *httptest.Server) {
	t.Helper()
	v := &f6Vendor{t: t, create: create, status: status}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.Method == http.MethodPost {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode request: %v", err)
			}
		}
		v.mu.Lock()
		v.calls = append(v.calls, f6Call{r.Method, r.URL.Path, r.Header.Clone(), body})
		v.mu.Unlock()
		out := v.status
		if r.Method == http.MethodPost {
			out = v.create
		}
		if _, err := io.WriteString(w, out); err != nil {
			t.Errorf("write vendor response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return v, srv
}

func (v *f6Vendor) snapshot() []f6Call {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]f6Call(nil), v.calls...)
}

func runImageBackendCase(t *testing.T, name string) {
	t.Helper()
	encoded, err := json.Marshal(imageResponse(name))
	if err != nil {
		t.Fatal(err)
	}
	v, srv := newF6Vendor(t, string(encoded), "")
	backend, err := NewImageBackend(name, srv.Client(), srv.URL+imagePath(name), f6Crypt)
	if err != nil {
		t.Fatalf("%s backend: %v", name, err)
	}
	if _, missErr := NewImageBackend(name, srv.Client(), srv.URL+imagePath(name), ""); !errors.Is(missErr, ErrModuleUnavailable) {
		t.Fatalf("%s without key = %v, want ErrModuleUnavailable", name, missErr)
	}
	model, _ := ImageModel(name)
	res, err := (&Service{Guard: &Guard{}}).Image(context.Background(), backend, &ImageRequest{
		IdempotencyKey: "idem-" + name, Model: model, Prompt: "a cat", Size: "1024x1024",
	})
	if err != nil {
		t.Fatalf("%s generate: %v", name, err)
	}
	assertImageWire(t, name, model, &res, v.snapshot())
}

func assertImageWire(t *testing.T, name, model string, res *ImageResult, calls []f6Call) {
	t.Helper()
	if res.Backend != name || res.Model != model || res.URL != "https://img" {
		t.Fatalf("%s result = %+v", name, *res)
	}
	if len(calls) != 1 {
		t.Fatalf("%s calls = %d, want 1", name, len(calls))
	}
	got := calls[0]
	wantAuth := "Bearer " + f6Crypt
	if name == "fal" {
		wantAuth = "Key " + f6Crypt
	}
	if got.method != http.MethodPost || got.path != imagePath(name) ||
		got.header.Get("Authorization") != wantAuth || got.header.Get("Idempotency-Key") != "idem-"+name {
		t.Fatalf("%s request = %s %s auth %q idem %q", name, got.method, got.path,
			got.header.Get("Authorization"), got.header.Get("Idempotency-Key"))
	}
	assertImagePayload(t, name, model, got.body)
	for _, v := range []any{res.URL, res.MIME, string(res.Data), res.Backend} {
		if strings.Contains(fmt.Sprint(v), f6Crypt) {
			t.Fatalf("%s leaked the key in %q", name, v)
		}
	}
}

func assertImagePayload(t *testing.T, name, model string, body map[string]any) {
	t.Helper()
	switch name {
	case "fal":
		assertFALImagePayload(t, body)
	case "openrouter":
		assertOpenRouterImagePayload(t, model, body)
	case "krea":
		assertKreaImagePayload(t, model, body)
	default:
		assertOpenAIStyleImagePayload(t, name, model, body)
	}
}

func assertFALImagePayload(t *testing.T, body map[string]any) {
	t.Helper()
	size, ok := body["image_size"].(map[string]any)
	if body["prompt"] != "a cat" || body["num_images"] != float64(1) ||
		!ok || size["width"] != float64(1024) || size["height"] != float64(1024) {
		t.Fatalf("fal payload = %v", body)
	}
}

func assertOpenRouterImagePayload(t *testing.T, model string, body map[string]any) {
	t.Helper()
	msgs, ok := body["messages"].([]any)
	if !ok || len(msgs) == 0 {
		t.Fatalf("openrouter messages = %v", body["messages"])
	}
	first, ok := msgs[0].(map[string]any)
	if !ok || first["content"] != "a cat" || body["model"] != model {
		t.Fatalf("openrouter message = %v", body["messages"])
	}
}

func assertKreaImagePayload(t *testing.T, model string, body map[string]any) {
	t.Helper()
	if body["model"] != model || body["prompt"] != "a cat" ||
		body["width"] != float64(1024) || body["height"] != float64(1024) {
		t.Fatalf("krea payload = %v", body)
	}
}

func assertOpenAIStyleImagePayload(t *testing.T, name, model string, body map[string]any) {
	t.Helper()
	if body["model"] != model || body["prompt"] != "a cat" ||
		body["size"] != "1024x1024" || body["n"] != float64(1) {
		t.Fatalf("%s payload = %v", name, body)
	}
}

func videoPath(name string) string {
	if name == "xai" {
		return "/v1/videos/generations"
	}
	return "/v1/videos"
}

func runVideoBackendCase(t *testing.T, name string) {
	t.Helper()
	v, srv := newF6Vendor(t, `{"request_id":"job1"}`, `{"status":"completed","video":{"url":"https://cdn.example/v.mp4"}}`)
	backend, err := NewVideoBackend(name, srv.Client(),
		srv.URL+videoPath(name), srv.URL+"/v1/videos/%s", f6Crypt)
	if err != nil {
		t.Fatalf("%s backend: %v", name, err)
	}
	if _, missErr := NewVideoBackend(name, srv.Client(), srv.URL+videoPath(name), srv.URL+"/v1/videos/%s", ""); !errors.Is(missErr, ErrModuleUnavailable) {
		t.Fatalf("%s without key = %v", name, missErr)
	}
	if hv, ok := backend.(*httpVideo); ok {
		hv.poll = time.Millisecond
	}
	model, _ := VideoModel(name)
	appr := &fakeApproval{answer: host.ApprovalAllowOnce}
	res, err := (&Service{Guard: &Guard{}, Approval: appr}).Video(context.Background(), backend, &VideoRequest{
		IdempotencyKey: "idem-" + name, Model: model, Prompt: "waves", DurationS: 5,
	})
	if err != nil {
		t.Fatalf("%s video: %v", name, err)
	}
	assertVideoWire(t, name, model, res, appr, v.snapshot())
}

func assertVideoWire(t *testing.T, name, model string, res VideoResult, appr *fakeApproval, calls []f6Call) {
	t.Helper()
	if res.Backend != name || res.URL != "https://cdn.example/v.mp4" || res.MIME != "video/mp4" {
		t.Fatalf("%s result = %+v", name, res)
	}
	if appr.req == nil || appr.req.Tool != "video_generate" || !strings.Contains(appr.req.Preview, "$") {
		t.Fatalf("%s approval = %+v", name, appr.req)
	}
	if len(calls) < 2 {
		t.Fatalf("%s calls = %d, want create+poll", name, len(calls))
	}
	create := calls[0]
	if create.method != http.MethodPost || create.path != videoPath(name) ||
		create.header.Get("Authorization") != "Bearer "+f6Crypt {
		t.Fatalf("%s create = %s %s", name, create.method, create.path)
	}
	if (name != "fal" && create.body["model"] != model) || create.body["prompt"] != "waves" ||
		create.body["duration"] != float64(5) {
		t.Fatalf("%s payload = %v", name, create.body)
	}
	assertVideoPolls(t, name, calls[1:])
}

func assertVideoPolls(t *testing.T, name string, polls []f6Call) {
	t.Helper()
	for _, c := range polls {
		if c.method != http.MethodGet || c.path != "/v1/videos/job1" || c.header.Get("Authorization") != "Bearer "+f6Crypt {
			t.Fatalf("%s poll = %s %s", name, c.method, c.path)
		}
	}
}

func TestF6PLUG020ImageGenBackends(t *testing.T) {
	t.Parallel()
	if got := ImageBackends(); len(got) != 8 {
		t.Fatalf("image backends = %v", got)
	}
	if _, err := NewImageBackend("mystery", noCallDoer{}, "http://example.test/x", f6Crypt); !errors.Is(err, ErrCapability) {
		t.Fatalf("unknown image backend = %v, want ErrCapability before egress", err)
	}
	for _, name := range ImageBackends() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			runImageBackendCase(t, name)
		})
	}
}

func TestF6PLUG027VideoGenBackends(t *testing.T) {
	t.Parallel()
	if got := VideoBackends(); len(got) != 4 {
		t.Fatalf("video backends = %v", got)
	}
	for _, name := range VideoBackends() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			runVideoBackendCase(t, name)
		})
	}
}

func TestF6MEDIA015ImageGeneration(t *testing.T) {
	t.Parallel()
	for _, name := range ImageBackends() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			runImageBackendCase(t, name)
		})
	}
}

func TestF6MEDIA016ImageFAL(t *testing.T)        { t.Parallel(); runImageBackendCase(t, "fal") }
func TestF6MEDIA017ImageOpenAI(t *testing.T)     { t.Parallel(); runImageBackendCase(t, "openai") }
func TestF6MEDIA018ImageCodex(t *testing.T)      { t.Parallel(); runImageBackendCase(t, "codex") }
func TestF6MEDIA019ImageOpenRouter(t *testing.T) { t.Parallel(); runImageBackendCase(t, "openrouter") }
func TestF6MEDIA020ImageXAI(t *testing.T)        { t.Parallel(); runImageBackendCase(t, "xai") }
func TestF6MEDIA021ImageDeepInfra(t *testing.T)  { t.Parallel(); runImageBackendCase(t, "deepinfra") }
func TestF6MEDIA022ImageKrea(t *testing.T)       { t.Parallel(); runImageBackendCase(t, "krea") }
func TestF6MEDIA023ImageMeta(t *testing.T)       { t.Parallel(); runImageBackendCase(t, "meta") }
func TestF6MEDIA025VideoFAL(t *testing.T)        { t.Parallel(); runVideoBackendCase(t, "fal") }
func TestF6MEDIA027VideoOpenRouter(t *testing.T) { t.Parallel(); runVideoBackendCase(t, "openrouter") }

func TestF6MEDIA028VideoDeepInfra(t *testing.T) { t.Parallel(); runVideoBackendCase(t, "deepinfra") }

func TestF6MEDIA024VideoGeneration(t *testing.T) {
	t.Parallel()
	for _, name := range VideoBackends() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			runVideoBackendCase(t, name)
		})
	}
}

// TestF6MEDIA026VideoXAI pins the xAI edit/extend wire in addition to
// generation: dedicated paths, source payload and a single poll.
func TestF6MEDIA026VideoXAI(t *testing.T) {
	t.Parallel()
	runVideoBackendCase(t, "xai")
	v, srv := newF6Vendor(t, `{"id":"job2"}`, `{"status":"done","video":{"url":"https://cdn.example/e.mp4"}}`)
	backend, err := NewXAIVideoEditor(srv.Client(), srv.URL+"/v1/videos/generations", srv.URL+"/v1/videos/%s", f6Crypt)
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{Guard: &Guard{}, Approval: &fakeApproval{answer: host.ApprovalAllowOnce}}
	for _, op := range []struct {
		name string
		want string
		fn   func(context.Context, *VideoRequest) (VideoResult, error)
	}{
		{"edit", "/v1/videos/edits", func(ctx context.Context, req *VideoRequest) (VideoResult, error) {
			return svc.EditVideo(ctx, backend, req, false)
		}},
		{"extend", "/v1/videos/extensions", func(ctx context.Context, req *VideoRequest) (VideoResult, error) {
			return svc.EditVideo(ctx, backend, req, true)
		}},
	} {
		res, err := op.fn(context.Background(), &VideoRequest{
			IdempotencyKey: "op-" + op.name, Model: "grok-imagine-video",
			Prompt: "glow", DurationS: 6, SourceURL: "https://cdn.example/src.mp4",
		})
		if err != nil || res.Backend != "xai" || res.URL != "https://cdn.example/e.mp4" {
			t.Fatalf("%s = %+v, %v", op.name, res, err)
		}
	}
	calls := v.snapshot()
	if len(calls) != 4 || calls[0].path != "/v1/videos/edits" || calls[2].path != "/v1/videos/extensions" {
		t.Fatalf("calls = %+v", calls)
	}
	video, ok := calls[2].body["video"].(map[string]any)
	if !ok || video["url"] != "https://cdn.example/src.mp4" || calls[2].body["duration"] != float64(6) {
		t.Fatalf("extend payload = %v", calls[2].body)
	}
}
