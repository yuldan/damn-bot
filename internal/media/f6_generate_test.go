//go:build !minimal

package media

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/damnbot/damnbot/internal/modules/host"
)

const f6ModuleCrypt = "f6-module-fixture-value"

type f6Request struct {
	method string
	path   string
	auth   string
	idem   string
	body   map[string]any
}

type f6Recorder struct {
	mu   sync.Mutex
	reqs []f6Request
}

func (r *f6Recorder) add(req *http.Request) map[string]any {
	var body map[string]any
	if req.Method == http.MethodPost {
		if decErr := json.NewDecoder(req.Body).Decode(&body); decErr != nil {
			body = map[string]any{"decode_error": decErr.Error()}
		}
	}
	r.mu.Lock()
	r.reqs = append(r.reqs, f6Request{
		method: req.Method, path: req.URL.Path,
		auth: req.Header.Get("Authorization"), idem: req.Header.Get("Idempotency-Key"), body: body,
	})
	r.mu.Unlock()
	return body
}

func (r *f6Recorder) snapshot() []f6Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]f6Request(nil), r.reqs...)
}

func f6Media(t *testing.T, cfg *Section, appr *fakeApprovals) (*Module, *f6Recorder) {
	t.Helper()
	rec := &f6Recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		switch r.URL.Path {
		case "/v1/images/generations":
			png := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nfixture"))
			writeVendor(t, w, `{"data":[{"b64_json":"`+png+`"}]}`)
		case "/v1/videos/generations", "/v1/videos/edits", "/v1/videos/extensions", "/create":
			writeVendor(t, w, `{"request_id":"job1"}`)
		default:
			writeVendor(t, w, `{"status":"done","video":{"url":"https://cdn.example/out.mp4"},"url":"https://cdn.example/out.mp4"}`)
		}
	}))
	t.Cleanup(srv.Close)
	cfg.Enabled = true
	if cfg.ImageEndpoint != "" {
		cfg.ImageEndpoint = srv.URL + cfg.ImageEndpoint
	}
	if cfg.VideoCreate != "" {
		cfg.VideoCreate = srv.URL + cfg.VideoCreate
	}
	if cfg.VideoStatus != "" {
		cfg.VideoStatus = srv.URL + cfg.VideoStatus
	}
	if appr == nil {
		appr = &fakeApprovals{answer: host.ApprovalAllowOnce}
	}
	return testModule(t, cfg, Deps{
		Approvals: appr,
		Secrets:   fakeSecrets{"MEDIA_KEY": f6ModuleCrypt},
	}, newMemStore()), rec
}

func writeVendor(t *testing.T, w io.Writer, body string) {
	t.Helper()
	if _, err := io.WriteString(w, body); err != nil {
		t.Errorf("write vendor response: %v", err)
	}
}

func hostCode(t *testing.T, err error, want string) {
	t.Helper()
	var he *host.Error
	if !errors.As(err, &he) || he.Code != want {
		t.Fatalf("err = %v, want code %q", err, want)
	}
}

func TestF6TOOL054ImageGenerate(t *testing.T) {
	t.Parallel()
	appr := &fakeApprovals{answer: host.ApprovalAllowOnce}
	m, rec := f6Media(t, &Section{ImageBackend: "openai", ImageEndpoint: "/v1/images/generations", ImageKeyEnv: "MEDIA_KEY"}, appr)
	args := json.RawMessage(`{"prompt":"cat","size":"1024x1024"}`)
	res, err := m.Invoke(context.Background(), ToolImageGenerate, args, host.Env{CallID: "img-1"})
	if err != nil {
		t.Fatalf("image_generate: %v", err)
	}
	if res.UntrustedSource != "media:image/openai" || res.Meta["backend"] != "openai" || !strings.Contains(res.Content, "bytes") {
		t.Fatalf("result = %+v", res)
	}
	reqs := rec.snapshot()
	if len(reqs) != 1 || reqs[0].path != "/v1/images/generations" || reqs[0].auth != "Bearer "+f6ModuleCrypt || reqs[0].idem == "" {
		t.Fatalf("vendor requests = %+v", reqs)
	}
	if len(appr.reqs) != 1 || appr.reqs[0].Tool != ToolImageGenerate ||
		!strings.Contains(appr.reqs[0].Preview, "$") || !strings.Contains(appr.reqs[0].Preview, "1024x1024") {
		t.Fatalf("approval = %+v", appr.reqs)
	}
	if _, repeatErr := m.Invoke(context.Background(), ToolImageGenerate, args, host.Env{CallID: "img-1"}); repeatErr != nil {
		t.Fatalf("repeat: %v", repeatErr)
	}
	if got := len(rec.snapshot()); got != 1 {
		t.Fatalf("repeat dispatched %d vendor calls, want the cached one", got)
	}
	assertImageNegatives(t, m, rec)
}

func assertImageNegatives(t *testing.T, m *Module, rec *f6Recorder) {
	t.Helper()
	if _, err := m.Invoke(context.Background(), ToolImageGenerate,
		json.RawMessage(`{"prompt":"cat","size":"-1x0"}`), host.Env{CallID: "img-2"}); err == nil {
		t.Fatal("invalid size accepted")
	}
	if got := len(rec.snapshot()); got != 1 {
		t.Fatalf("invalid size dialed the vendor (%d calls)", got)
	}
	if _, err := New(&Section{ImageBackend: "mystery", ImageKeyEnv: "MEDIA_KEY"}, Deps{}); !errors.Is(err, errBadConfig) {
		t.Fatalf("unknown backend config = %v, want errBadConfig", err)
	}
}

func TestF6TOOL082VideoGenerate(t *testing.T) {
	t.Parallel()
	appr := &fakeApprovals{answer: host.ApprovalAllowOnce}
	m, rec := f6Media(t, &Section{
		VideoBackend: "fal", VideoKeyEnv: "MEDIA_KEY",
		VideoCreate: "/create", VideoStatus: "/v/%s",
	}, appr)
	args := json.RawMessage(`{"prompt":"waves","duration_s":4}`)
	res, err := m.Invoke(context.Background(), ToolVideoGenerate, args, host.Env{CallID: "vid-1"})
	if err != nil || res.UntrustedSource != "media:video/fal" || res.Meta["backend"] != "fal" {
		t.Fatalf("video_generate = %+v, %v", res, err)
	}
	if len(appr.reqs) != 1 || appr.reqs[0].Tool != ToolVideoGenerate || !strings.Contains(appr.reqs[0].Preview, "$") {
		t.Fatalf("approval = %+v", appr.reqs)
	}
	reqs := rec.snapshot()
	if len(reqs) < 2 || reqs[0].path != "/create" || reqs[0].idem == "" || reqs[0].body["duration"] != float64(4) {
		t.Fatalf("vendor requests = %+v", reqs)
	}
	if _, err := m.Invoke(context.Background(), ToolVideoGenerate, args, host.Env{CallID: "vid-1"}); err != nil {
		t.Fatalf("repeat: %v", err)
	}
	if got := len(rec.snapshot()); got != len(reqs) {
		t.Fatalf("repeat created a second job: %d calls, was %d", got, len(reqs))
	}

	// Uncertain outcome: one create, never a retry, mapped to `network`.
	var creates atomic.Int32
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		creates.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failing.Close()
	um := testModule(t, &Section{
		Enabled: true, VideoBackend: "fal", VideoKeyEnv: "MEDIA_KEY",
		VideoCreate: failing.URL + "/create", VideoStatus: failing.URL + "/v/%s",
	}, Deps{}, newMemStore())
	for range 2 {
		_, err := um.Invoke(context.Background(), ToolVideoGenerate, args, host.Env{CallID: "vid-uncertain"})
		hostCode(t, err, "network")
	}
	if creates.Load() != 1 {
		t.Fatalf("uncertain outcome retried: %d creates", creates.Load())
	}
}

func xaiEditModule(t *testing.T, appr *fakeApprovals) (*Module, *f6Recorder) {
	t.Helper()
	return f6Media(t, &Section{
		VideoBackend: "xai", VideoKeyEnv: "MEDIA_KEY",
		VideoCreate: "/v1/videos/generations", VideoStatus: "/v1/videos/%s",
	}, appr)
}

func TestF6TOOL083XAIVideoEdit(t *testing.T) {
	t.Parallel()
	appr := &fakeApprovals{answer: host.ApprovalAllowOnce}
	m, rec := xaiEditModule(t, appr)
	args := json.RawMessage(`{"prompt":"add a hat","video_url":"https://cdn.example/in.mp4"}`)
	res, err := m.Invoke(context.Background(), ToolXAIVideoEdit, args, host.Env{CallID: "edit-1"})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	assertEditWire(t, res, appr, rec.snapshot())
	if _, repeatErr := m.Invoke(context.Background(), ToolXAIVideoEdit, args, host.Env{CallID: "edit-1"}); repeatErr != nil {
		t.Fatalf("repeat: %v", repeatErr)
	}
	if got := len(rec.snapshot()); got != 2 {
		t.Fatalf("repeat dispatched a second edit: %d calls", got)
	}
	assertEditNegatives(t, m, rec)
	assertNoSecret(t, res, appr, args)
}

func assertEditWire(t *testing.T, res host.Result, appr *fakeApprovals, reqs []f6Request) {
	t.Helper()
	if res.Meta["backend"] != "xai" || res.UntrustedSource != "media:video/xai" {
		t.Fatalf("edit = %+v", res)
	}
	if len(reqs) != 2 || reqs[0].path != "/v1/videos/edits" || reqs[0].method != http.MethodPost {
		t.Fatalf("vendor requests = %+v", reqs)
	}
	video, ok := reqs[0].body["video"].(map[string]any)
	if !ok || reqs[0].body["prompt"] != "add a hat" || video["url"] != "https://cdn.example/in.mp4" ||
		reqs[0].body["model"] != "grok-imagine-video" || reqs[0].auth != "Bearer "+f6ModuleCrypt {
		t.Fatalf("edit payload = %+v", reqs[0])
	}
	if len(appr.reqs) != 1 || appr.reqs[0].Tool != ToolXAIVideoEdit || !strings.Contains(appr.reqs[0].Preview, "$") {
		t.Fatalf("approval = %+v", appr.reqs)
	}
}

func assertEditNegatives(t *testing.T, m *Module, rec *f6Recorder) {
	t.Helper()
	for _, bad := range []json.RawMessage{
		json.RawMessage(`{"prompt":"add a hat"}`),
		json.RawMessage(`{"prompt":"add a hat","video_url":"http://cdn.example/in.mp4"}`),
		json.RawMessage(`{"prompt":"add a hat","video_url":"https://cdn.example/in.mov"}`),
	} {
		if _, err := m.Invoke(context.Background(), ToolXAIVideoEdit, bad, host.Env{CallID: "edit-bad"}); err == nil {
			t.Fatalf("invalid args accepted: %s", bad)
		} else {
			hostCode(t, err, "invalid_args")
		}
	}
	if got := len(rec.snapshot()); got != 2 {
		t.Fatalf("invalid args dialed the vendor (%d calls)", got)
	}
	denied, drec := xaiEditModule(t, &fakeApprovals{answer: host.ApprovalDeny})
	if _, err := denied.Invoke(context.Background(), ToolXAIVideoEdit,
		json.RawMessage(`{"prompt":"add a hat","video_url":"https://cdn.example/in.mp4"}`), host.Env{CallID: "edit-denied"}); err == nil {
		t.Fatal("denied approval accepted")
	} else {
		hostCode(t, err, "denied")
	}
	if got := len(drec.snapshot()); got != 0 {
		t.Fatalf("denied edit dialed the vendor (%d calls)", got)
	}
	fal, frec := f6Media(t, &Section{
		VideoBackend: "fal", VideoKeyEnv: "MEDIA_KEY",
		VideoCreate: "/create", VideoStatus: "/v/%s",
	}, nil)
	if _, err := fal.Invoke(context.Background(), ToolXAIVideoEdit,
		json.RawMessage(`{"prompt":"add a hat","video_url":"https://cdn.example/in.mp4"}`), host.Env{CallID: "edit-fal"}); err == nil {
		t.Fatal("non-xAI backend served an edit")
	} else {
		hostCode(t, err, "module_unavailable")
	}
	if got := len(frec.snapshot()); got != 0 {
		t.Fatalf("non-xAI backend dialed the vendor (%d calls)", got)
	}
}

func assertNoSecret(t *testing.T, res host.Result, appr *fakeApprovals, args json.RawMessage) {
	t.Helper()
	for _, text := range []string{res.Content, appr.reqs[0].Preview, appr.reqs[0].Class, string(args)} {
		if strings.Contains(text, f6ModuleCrypt) {
			t.Fatalf("key leaked into %q", text)
		}
	}
	for k, v := range res.Meta {
		if strings.Contains(toText(v), f6ModuleCrypt) {
			t.Fatalf("key leaked into meta %q", k)
		}
	}
}

func TestF6TOOL084XAIVideoExtend(t *testing.T) {
	t.Parallel()
	appr := &fakeApprovals{answer: host.ApprovalAllowOnce}
	m, rec := xaiEditModule(t, appr)
	args := json.RawMessage(`{"prompt":"keep going","video_url":"https://cdn.example/in.mp4","duration_s":6}`)
	res, err := m.Invoke(context.Background(), ToolXAIVideoExtend, args, host.Env{CallID: "ext-1"})
	if err != nil || res.Meta["backend"] != "xai" {
		t.Fatalf("extend = %+v, %v", res, err)
	}
	reqs := rec.snapshot()
	if len(reqs) != 2 || reqs[0].path != "/v1/videos/extensions" || reqs[0].body["duration"] != float64(6) {
		t.Fatalf("extend requests = %+v", reqs)
	}
	if len(appr.reqs) != 1 || appr.reqs[0].Tool != ToolXAIVideoExtend || !strings.Contains(appr.reqs[0].Preview, "6 s") {
		t.Fatalf("approval = %+v", appr.reqs)
	}
	if _, err := m.Invoke(context.Background(), ToolXAIVideoExtend,
		json.RawMessage(`{"prompt":"keep going","video_url":"https://cdn.example/in.mp4"}`), host.Env{CallID: "ext-default"}); err != nil {
		t.Fatalf("default duration: %v", err)
	}
	reqs = rec.snapshot()
	if reqs[2].body["duration"] != float64(6) {
		t.Fatalf("default extension duration = %v", reqs[2].body["duration"])
	}
	for _, bad := range []json.RawMessage{
		json.RawMessage(`{"prompt":"keep going","video_url":"https://cdn.example/in.mp4","duration_s":11}`),
		json.RawMessage(`{"prompt":"keep going","video_url":"https://cdn.example/in.mp4","duration_s":1}`),
		json.RawMessage(`{"prompt":"keep going","video_url":""}`),
	} {
		_, err := m.Invoke(context.Background(), ToolXAIVideoExtend, bad, host.Env{CallID: "ext-bad"})
		hostCode(t, err, "invalid_args")
	}
	if got := len(rec.snapshot()); got != 4 {
		t.Fatalf("invalid extension dialed the vendor (%d calls)", got)
	}
}

func toText(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
