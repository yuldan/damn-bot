package gen

import (
	"context"
	"encoding/base64"
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

func pngFixture() []byte { return []byte("\x89PNG\r\n\x1a\nfixture") }

type fakeImage struct {
	calls int32
	price Price
	known bool
	err   error
}

func (f *fakeImage) Name() string               { return "fake-image" }
func (f *fakeImage) Models() []string           { return []string{"fake-model"} }
func (f *fakeImage) Price(string) (Price, bool) { return f.price, f.known }
func (f *fakeImage) Generate(context.Context, *ImageRequest) (ImageResult, error) {
	atomic.AddInt32(&f.calls, 1)
	if f.err != nil {
		return ImageResult{}, f.err
	}
	return ImageResult{Backend: f.Name(), Data: pngFixture()}, nil
}

type fakeVideo struct {
	calls int32
	price Price
	err   error
}

func (f *fakeVideo) Name() string               { return "fake-video" }
func (f *fakeVideo) Models() []string           { return []string{"fake-video-model"} }
func (f *fakeVideo) Price(string) (Price, bool) { return f.price, true }
func (f *fakeVideo) Generate(context.Context, *VideoRequest) (VideoResult, error) {
	atomic.AddInt32(&f.calls, 1)
	if f.err != nil {
		return VideoResult{}, f.err
	}
	return VideoResult{Backend: f.Name(), URL: "https://example/v.mp4"}, nil
}

type fakeAnalyzer struct {
	calls int32
	price Price
}

func (f *fakeAnalyzer) Name() string               { return "fake-analyze" }
func (f *fakeAnalyzer) Models() []string           { return []string{"fake-video-model"} }
func (f *fakeAnalyzer) Price(string) (Price, bool) { return f.price, true }
func (f *fakeAnalyzer) Analyze(context.Context, *VideoRequest) (VideoAnalysis, error) {
	atomic.AddInt32(&f.calls, 1)
	return VideoAnalysis{Backend: f.Name(), Text: "a cat"}, nil
}

type fakeApproval struct {
	calls  int32
	req    *host.ApprovalRequest
	answer host.Approval
	err    error
}

func (f *fakeApproval) Approve(_ context.Context, req *host.ApprovalRequest) (host.Approval, error) {
	atomic.AddInt32(&f.calls, 1)
	f.req = req
	return f.answer, f.err
}

func staticSpend(v int64) Spend {
	return func(context.Context, time.Time, time.Time) (int64, error) { return v, nil }
}

func videoPrice() Price {
	return Price{PerSecond: 1_000}
}

func imagePriceFixture() Price {
	return imagePrice(0, 0, map[string]int64{"1024x1024": 40_000})
}

func TestBackendsHTTPOpenAI(t *testing.T) {
	t.Parallel()
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.Header.Get("Authorization") != "Bearer key" || r.Header.Get("Idempotency-Key") != "idem-1" {
			t.Errorf("openai headers = %v", r.Header)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["size"] != "1024x1024" {
			t.Errorf("openai body = %v, %v", body, err)
		}
		if err := json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString(pngFixture())}},
		}); err != nil {
			t.Errorf("encode: %v", err)
		}
	}))
	defer server.Close()
	backend, err := NewOpenAIImages(server.Client(), server.URL, "key")
	if err != nil {
		t.Fatal(err)
	}
	if _, errMissing := NewOpenAIImages(server.Client(), server.URL, ""); !errors.Is(errMissing, ErrModuleUnavailable) {
		t.Fatalf("missing key = %v", errMissing)
	}
	res, err := (&Service{Guard: &Guard{}}).Image(context.Background(), backend, &ImageRequest{
		IdempotencyKey: "idem-1", Model: "gpt-image-1", Prompt: "cat", Size: "1024x1024",
	})
	if err != nil || len(res.Data) != len(pngFixture()) || res.MIME != "image/png" || calls != 1 {
		t.Fatalf("openai = %+v, %v, calls %d", res, err, calls)
	}
}

func TestBackendsHTTPFAL(t *testing.T) {
	t.Parallel()
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.Header.Get("Authorization") != "Key key" {
			t.Errorf("fal auth = %q", r.Header.Get("Authorization"))
		}
		if err := json.NewEncoder(w).Encode(map[string]any{
			"images": []map[string]string{{"url": "https://fal/img.png", "mime_type": "image/png"}},
		}); err != nil {
			t.Errorf("encode: %v", err)
		}
	}))
	defer server.Close()
	backend, err := NewFALImages(server.Client(), server.URL, "key")
	if err != nil {
		t.Fatal(err)
	}
	res, err := (&Service{Guard: &Guard{}}).Image(context.Background(), backend, &ImageRequest{
		IdempotencyKey: "idem-2", Model: "fal-ai/flux/dev", Prompt: "cat", Size: "1024x1024",
	})
	if err != nil || res.URL != "https://fal/img.png" || res.MIME != "image/png" || calls != 1 {
		t.Fatalf("fal = %+v, %v, calls %d", res, err, calls)
	}
}

func TestBudgetGuardTools(t *testing.T) {
	t.Parallel()
	img := &fakeImage{price: imagePriceFixture(), known: true}
	svc := &Service{Guard: &Guard{Caps: Caps{DailyMicros: 100}, Spend: staticSpend(100)}}
	if _, err := svc.Image(context.Background(), img, &ImageRequest{
		IdempotencyKey: "cap", Model: "fake-model", Prompt: "x", Size: "1024x1024",
	}); !errors.Is(err, ErrBudget) {
		t.Fatalf("daily cap = %v", err)
	}
	if img.calls != 0 {
		t.Fatalf("vendor calls after cap = %d", img.calls)
	}

	warned := ""
	session := &Guard{
		Caps: Caps{SessionMicros: 100, DailyMicros: 1000}, Spend: staticSpend(0),
		Warn: func(p string, _, _ int64) { warned = p },
	}
	session.Record(100)
	if err := session.Check(context.Background(), 1); !errors.Is(err, ErrBudget) {
		t.Fatalf("session cap = %v", err)
	}
	guard := &Guard{Caps: Caps{SessionMicros: 100}, Warn: func(p string, _, _ int64) { warned = p }}
	if err := guard.Check(context.Background(), 80); err != nil || warned != "session" {
		t.Fatalf("warn = %q, %v", warned, err)
	}
}

func TestServiceIdempotentImage(t *testing.T) {
	t.Parallel()
	img := &fakeImage{price: imagePriceFixture(), known: true}
	svc := &Service{Guard: &Guard{}}
	req := ImageRequest{IdempotencyKey: "same", Model: "fake-model", Prompt: "x", Size: "1024x1024"}
	first, err := svc.Image(context.Background(), img, &req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Image(context.Background(), img, &req)
	if err != nil || second.Backend != first.Backend {
		t.Fatalf("retry = %+v, %v (first %+v)", second, err, first)
	}
	if img.calls != 1 {
		t.Fatalf("vendor calls = %d", img.calls)
	}

	uncertain := &fakeImage{price: imagePriceFixture(), known: true, err: fmt.Errorf("%w: drop", ErrUncertain)}
	if _, err := svc.Image(context.Background(), uncertain, &ImageRequest{
		IdempotencyKey: "u", Model: "fake-model", Prompt: "x", Size: "1024x1024",
	}); !errors.Is(err, ErrUncertain) {
		t.Fatalf("uncertain = %v", err)
	}
	if _, err := svc.Image(context.Background(), uncertain, &ImageRequest{
		IdempotencyKey: "u", Model: "fake-model", Prompt: "x", Size: "1024x1024",
	}); !errors.Is(err, ErrUncertain) || uncertain.calls != 1 {
		t.Fatalf("uncertain retry = %v, calls %d", err, uncertain.calls)
	}

	unknown := &fakeImage{known: false}
	if _, err := svc.Image(context.Background(), unknown, &req); !errors.Is(err, ErrCapability) || unknown.calls != 0 {
		t.Fatalf("unknown price = %v, calls %d", err, unknown.calls)
	}
}

func TestF6COST11VideoIdempotency(t *testing.T) {
	t.Parallel()
	approval := &fakeApproval{answer: host.ApprovalAllowOnce}
	video := &fakeVideo{price: videoPrice()}
	svc := &Service{Guard: &Guard{}, Approval: approval}
	req := VideoRequest{IdempotencyKey: "v1", Model: "fake-video-model", Prompt: "x", DurationS: 5}
	if _, err := svc.Video(context.Background(), video, &req); err != nil {
		t.Fatal(err)
	}
	if video.calls != 1 || approval.req == nil || approval.req.Effect != host.EffectNonIdempotent ||
		!strings.Contains(approval.req.Preview, "$") {
		t.Fatalf("approval/video = %+v, calls %d", approval.req, video.calls)
	}
	if _, err := svc.Video(context.Background(), video, &req); err != nil || video.calls != 1 || approval.calls != 1 {
		t.Fatalf("cached video = %v, calls %d/%d", err, video.calls, approval.calls)
	}

	drop := &fakeVideo{price: videoPrice(), err: fmt.Errorf("%w: network", ErrUncertain)}
	if _, err := svc.Video(context.Background(), drop, &VideoRequest{
		IdempotencyKey: "v2", Model: "fake-video-model", Prompt: "x", DurationS: 5,
	}); !errors.Is(err, ErrUncertain) {
		t.Fatalf("uncertain video = %v", err)
	}
	if _, err := svc.Video(context.Background(), drop, &VideoRequest{
		IdempotencyKey: "v2", Model: "fake-video-model", Prompt: "x", DurationS: 5,
	}); !errors.Is(err, ErrUncertain) || drop.calls != 1 {
		t.Fatalf("uncertain retry = %v, calls %d", err, drop.calls)
	}
}

func TestVideoApprovalDenied(t *testing.T) {
	t.Parallel()
	denied := &fakeApproval{answer: host.ApprovalDeny}
	video := &fakeVideo{price: videoPrice()}
	svc := &Service{Guard: &Guard{}, Approval: denied}
	if _, err := svc.Video(context.Background(), video, &VideoRequest{
		IdempotencyKey: "v3", Model: "fake-video-model", Prompt: "x", DurationS: 5,
	}); !errors.Is(err, ErrApprovalDenied) || video.calls != 0 {
		t.Fatalf("deny = %v, calls %d", err, video.calls)
	}
}

func TestVideoBudgetCap(t *testing.T) {
	t.Parallel()
	video := &fakeVideo{price: videoPrice()}
	svc := &Service{
		Guard:    &Guard{Caps: Caps{DailyMicros: 100}, Spend: staticSpend(100)},
		Approval: &fakeApproval{answer: host.ApprovalAllowOnce},
	}
	if _, err := svc.Video(context.Background(), video, &VideoRequest{
		IdempotencyKey: "v4", Model: "fake-video-model", Prompt: "x", DurationS: 5,
	}); !errors.Is(err, ErrBudget) || video.calls != 0 {
		t.Fatalf("cap = %v, calls %d", err, video.calls)
	}
}

func TestVideoAnalyze(t *testing.T) {
	t.Parallel()
	a := &fakeAnalyzer{price: Price{InputPerMTok: 1_000_000}}
	svc := &Service{Guard: &Guard{}}
	res, err := svc.AnalyzeVideo(context.Background(), a, &VideoRequest{
		IdempotencyKey: "a1", Model: "fake-video-model", Prompt: "describe",
	})
	if err != nil || res.Text != "a cat" || a.calls != 1 {
		t.Fatalf("analyze = %+v, %v, calls %d", res, err, a.calls)
	}
	if _, err := svc.AnalyzeVideo(context.Background(), &fakeAnalyzer{}, &VideoRequest{
		IdempotencyKey: "a2", Model: "fake-video-model", Prompt: "x",
	}); !errors.Is(err, ErrCapability) {
		t.Fatalf("unpriced analysis = %v", err)
	}
}
