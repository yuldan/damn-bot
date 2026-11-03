package gen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/modules/host"
)

func imageModels() map[string]string {
	return map[string]string{
		"openai": "gpt-image-1", "fal": "fal-ai/flux/dev", "codex": "gpt-image-1",
		"openrouter": "google/gemini-2.5-flash-image", "xai": "grok-imagine-image",
		"deepinfra": "black-forest-labs/FLUX-1-schnell", "krea": "krea-2", "meta": "muse-image",
	}
}

func videoModels() map[string]string {
	return map[string]string{
		"fal": "fal-ai/veo3", "xai": "grok-imagine-video",
		"openrouter": "google/veo-3", "deepinfra": "Wan-AI/Wan2.1-T2V-14B",
	}
}

func imageResponse(name string) any {
	switch name {
	case "fal":
		return map[string]any{"images": []map[string]string{{"url": "https://img", "mime_type": "image/png"}}}
	case "openrouter":
		return map[string]any{"choices": []map[string]any{{
			"message": map[string]any{"images": []map[string]any{{
				"image_url": map[string]string{"url": "https://img"},
			}}},
		}}}
	case "krea":
		return map[string]any{"url": "https://img"}
	default:
		return map[string]any{"data": []map[string]string{{"url": "https://img"}}}
	}
}

func imageServer(t *testing.T, name string, status int, delay time.Duration, calls *int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		if err := json.NewEncoder(w).Encode(imageResponse(name)); err != nil {
			t.Errorf("encode: %v", err)
		}
	}))
}

type noCallDoer struct{}

func (noCallDoer) Do(*http.Request) (*http.Response, error) {
	return nil, errors.New("unexpected egress in catalog test")
}

func TestClosedBackendCatalog(t *testing.T) {
	t.Parallel()
	images := ImageBackends()
	if len(images) != 8 {
		t.Fatalf("image backends = %v", images)
	}
	for _, name := range images {
		if _, err := NewImageBackend(name, nil, "http://x", ""); !errors.Is(err, ErrModuleUnavailable) {
			t.Fatalf("image %s without key = %v", name, err)
		}
		if _, err := NewImageBackend(name, noCallDoer{}, "http://x", "k"); err != nil {
			t.Fatalf("image %s = %v", name, err)
		}
	}
	if _, err := NewImageBackend("nope", noCallDoer{}, "http://x", "k"); !errors.Is(err, ErrCapability) {
		t.Fatalf("unknown image = %v", err)
	}
	videos := VideoBackends()
	if len(videos) != 4 {
		t.Fatalf("video backends = %v", videos)
	}
	for _, name := range videos {
		if _, err := NewVideoBackend(name, nil, "http://x", "http://x/%s", ""); !errors.Is(err, ErrModuleUnavailable) {
			t.Fatalf("video %s without key = %v", name, err)
		}
		if _, err := NewVideoBackend(name, noCallDoer{}, "http://x", "http://x/%s", "k"); err != nil {
			t.Fatalf("video %s = %v", name, err)
		}
	}
	if _, err := NewVideoBackend("nope", noCallDoer{}, "http://x", "http://x/%s", "k"); !errors.Is(err, ErrCapability) {
		t.Fatalf("unknown video = %v", err)
	}
}

func TestImageBackendsHTTPAll(t *testing.T) {
	t.Parallel()
	for _, name := range ImageBackends() {
		t.Run(name, func(t *testing.T) {
			var calls int32
			srv := imageServer(t, name, http.StatusOK, 0, &calls)
			defer srv.Close()
			backend, err := NewImageBackend(name, srv.Client(), srv.URL, "key")
			if err != nil {
				t.Fatal(err)
			}
			model := imageModels()[name]
			res, err := (&Service{Guard: &Guard{}}).Image(context.Background(), backend, &ImageRequest{
				IdempotencyKey: "k-" + name, Model: model, Prompt: "cat", Size: "1024x1024",
			})
			if err != nil || res.URL != "https://img" || calls != 1 {
				t.Fatalf("%s = %+v, %v, calls %d", name, res, err, calls)
			}
		})
	}
}

func TestImageBackendsErrors(t *testing.T) {
	testErrorMatrix(t, ImageBackends(), testImageError, testImageTimeout)
}

func testVideoError(t *testing.T, name string, status int) {
	var calls int32
	srv := videoServer(t, status, 0, &calls)
	defer srv.Close()
	backend, err := NewVideoBackend(name, srv.Client(), srv.URL+"/create", srv.URL+"/status/%s", "key")
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{Guard: &Guard{}, Approval: &fakeApproval{answer: host.ApprovalAllowOnce}}
	req := &VideoRequest{IdempotencyKey: "e", Model: videoModels()[name], Prompt: "x", DurationS: 5}
	if _, err := svc.Video(context.Background(), backend, req); !errors.Is(err, ErrUncertain) {
		t.Fatalf("%s status %d = %v", name, status, err)
	}
	if _, err := svc.Video(context.Background(), backend, req); !errors.Is(err, ErrUncertain) || calls != 1 {
		t.Fatalf("%s uncertain retry = %v, calls %d", name, err, calls)
	}
}

func testVideoTimeout(t *testing.T, name string) {
	var calls int32
	srv := videoServer(t, http.StatusOK, 30*time.Millisecond, &calls)
	defer srv.Close()
	backend, err := NewVideoBackend(name, srv.Client(), srv.URL+"/create", srv.URL+"/status/%s", "key")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	_, err = (&Service{Guard: &Guard{}, Approval: &fakeApproval{answer: host.ApprovalAllowOnce}}).Video(ctx, backend, &VideoRequest{
		IdempotencyKey: "t", Model: videoModels()[name], Prompt: "x", DurationS: 5,
	})
	if !errors.Is(err, ErrUncertain) {
		t.Fatalf("%s timeout = %v", name, err)
	}
}

func testImageError(t *testing.T, name string, status int) {
	var calls int32
	srv := imageServer(t, name, status, 0, &calls)
	defer srv.Close()
	backend, err := NewImageBackend(name, srv.Client(), srv.URL, "key")
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{Guard: &Guard{}}
	req := &ImageRequest{IdempotencyKey: "e", Model: imageModels()[name], Prompt: "x", Size: "1024x1024"}
	if _, err := svc.Image(context.Background(), backend, req); !errors.Is(err, ErrUncertain) {
		t.Fatalf("%s status %d = %v", name, status, err)
	}
	if _, err := svc.Image(context.Background(), backend, req); !errors.Is(err, ErrUncertain) || calls != 1 {
		t.Fatalf("%s uncertain retry = %v, calls %d", name, err, calls)
	}
}

func testImageTimeout(t *testing.T, name string) {
	var calls int32
	srv := imageServer(t, name, http.StatusOK, 30*time.Millisecond, &calls)
	defer srv.Close()
	backend, err := NewImageBackend(name, srv.Client(), srv.URL, "key")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	_, err = (&Service{Guard: &Guard{}}).Image(ctx, backend, &ImageRequest{
		IdempotencyKey: "t", Model: imageModels()[name], Prompt: "x", Size: "1024x1024",
	})
	if !errors.Is(err, ErrUncertain) {
		t.Fatalf("%s timeout = %v", name, err)
	}
}

func videoServer(t *testing.T, status int, delay time.Duration, calls *int32) *httptest.Server {
	t.Helper()
	var polls atomic.Int32
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		if r.Method == http.MethodPost {
			if err := json.NewEncoder(w).Encode(map[string]string{"request_id": "job-1"}); err != nil {
				t.Errorf("encode create: %v", err)
			}
			return
		}
		if polls.Add(1) == 1 {
			if err := json.NewEncoder(w).Encode(map[string]string{"status": "processing"}); err != nil {
				t.Errorf("encode pending: %v", err)
			}
			return
		}
		if err := json.NewEncoder(w).Encode(map[string]string{"status": "completed", "url": "https://video"}); err != nil {
			t.Errorf("encode done: %v", err)
		}
	}))
}

func TestVideoBackendsPolling(t *testing.T) {
	t.Parallel()
	for _, name := range VideoBackends() {
		t.Run(name, func(t *testing.T) {
			var calls int32
			srv := videoServer(t, http.StatusOK, 0, &calls)
			defer srv.Close()
			backend, err := NewVideoBackend(name, srv.Client(), srv.URL+"/create", srv.URL+"/status/%s", "key")
			if err != nil {
				t.Fatal(err)
			}
			if hv, ok := backend.(*httpVideo); ok {
				hv.poll = time.Millisecond
			}
			approval := &fakeApproval{answer: host.ApprovalAllowOnce}
			res, err := (&Service{Guard: &Guard{}, Approval: approval}).Video(context.Background(), backend, &VideoRequest{
				IdempotencyKey: "v-" + name, Model: videoModels()[name], Prompt: "x", DurationS: 5,
			})
			if err != nil || res.URL != "https://video" || calls < 3 {
				t.Fatalf("%s = %+v, %v, calls %d", name, res, err, calls)
			}
			if approval.req == nil || approval.req.Effect != host.EffectNonIdempotent ||
				!strings.Contains(approval.req.Preview, "$") {
				t.Fatalf("%s approval = %+v", name, approval.req)
			}
		})
	}
}

func TestVideoBackendsErrors(t *testing.T) {
	testErrorMatrix(t, VideoBackends(), testVideoError, testVideoTimeout)
}

func testErrorMatrix(t *testing.T, names []string, errFn func(*testing.T, string, int), timeoutFn func(*testing.T, string)) {
	t.Parallel()
	for _, name := range names {
		for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError} {
			t.Run(fmt.Sprintf("%s/%d", name, status), func(t *testing.T) {
				t.Parallel()
				errFn(t, name, status)
			})
		}
		t.Run(name+"/timeout", func(t *testing.T) {
			t.Parallel()
			timeoutFn(t, name)
		})
	}
}
