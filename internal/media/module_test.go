//go:build !minimal

package media

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/media/gen"
	"github.com/damnbot/damnbot/internal/modules/host"
)

type memStore struct {
	mu sync.Mutex
	m  map[string][]byte
}

func newMemStore() *memStore { return &memStore{m: map[string][]byte{}} }

func (s *memStore) Get(_ context.Context, key []byte) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[string(key)]
	return v, ok, nil
}

func (s *memStore) Put(_ context.Context, key, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[string(key)] = append([]byte(nil), value...)
	return nil
}

func (s *memStore) Delete(_ context.Context, key []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, string(key))
	return nil
}

func (s *memStore) List(_ context.Context, prefix []byte) ([][]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out [][]byte
	for k := range s.m {
		if strings.HasPrefix(k, string(prefix)) {
			out = append(out, []byte(k))
		}
	}
	return out, nil
}

type fakeApprovals struct {
	mu     sync.Mutex
	answer host.Approval
	reqs   []*host.ApprovalRequest
}

func (f *fakeApprovals) Approve(_ context.Context, req *host.ApprovalRequest) (host.Approval, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, req)
	return f.answer, nil
}

type fakeSecrets map[string]string

func (f fakeSecrets) ChildEnv(p host.AllowPolicy) ([]string, error) {
	var out []string
	for _, name := range p.Inherit {
		if v := f[name]; v != "" {
			out = append(out, name+"="+v)
		}
	}
	return out, nil
}

func testModule(t *testing.T, cfg *Section, deps Deps, store host.Store) *Module {
	t.Helper()
	if deps.Approvals == nil {
		deps.Approvals = &fakeApprovals{answer: host.ApprovalAllowOnce}
	}
	if deps.Secrets == nil {
		deps.Secrets = fakeSecrets{"MEDIA_KEY": "k"}
	}
	m, err := New(cfg, deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	m.store = store
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return m
}

func TestModuleNewRejectsBadPairing(t *testing.T) {
	t.Parallel()
	for _, cfg := range []Section{
		{Enabled: true, ImageBackend: "nope"},
		{Enabled: true, ImageBackend: "openai"},
		{Enabled: true, VideoBackend: "fal"},
		{Enabled: true, VideoBackend: "fal", VideoKeyEnv: "MEDIA_KEY", VideoCreate: "http://x"},
	} {
		if _, err := New(&cfg, Deps{}); !errors.Is(err, errBadConfig) {
			t.Errorf("New(%+v) = %v, want errBadConfig", cfg, err)
		}
	}
}

func TestModuleDocReadTaints(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello media"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := testModule(t, &Section{Enabled: true}, Deps{}, nil)
	res, err := m.Invoke(context.Background(), ToolDocRead, json.RawMessage(`{"path":"a.txt"}`), host.Env{Cwd: dir})
	if err != nil || !strings.Contains(res.Content, "hello media") || res.UntrustedSource != "media:doc" {
		t.Fatalf("doc_read = %+v, %v", res, err)
	}
}

func TestModuleDocWriteApproval(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		answer host.Approval
		want   bool
	}{
		{"denied", host.ApprovalDeny, false},
		{"session is not enough", host.ApprovalAllowSession, false},
		{"allow once", host.ApprovalAllowOnce, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			checkDocWrite(t, tc.answer, tc.want)
		})
	}
}

func checkDocWrite(t *testing.T, answer host.Approval, want bool) {
	t.Helper()
	dir := t.TempDir()
	appr := &fakeApprovals{answer: answer}
	m := testModule(t, &Section{Enabled: true}, Deps{Approvals: appr}, nil)
	args := json.RawMessage(`{"path":"out.md","content":"secret body"}`)
	_, err := m.Invoke(context.Background(), ToolDocWrite, args, host.Env{Cwd: dir, CallID: "c1"})
	if want {
		if err != nil {
			t.Fatalf("doc_write: %v", err)
		}
	} else {
		var he *host.Error
		if !errors.As(err, &he) || he.Code != "denied" {
			t.Fatalf("err = %v, want denied", err)
		}
	}
	_, statErr := os.Stat(filepath.Join(dir, "out.md"))
	if (statErr == nil) != want {
		t.Fatalf("file exists = %v, want %v", statErr == nil, want)
	}
	if len(appr.reqs) != 1 || strings.Contains(appr.reqs[0].Preview, "secret body") {
		t.Fatalf("approval preview leaked content: %+v", appr.reqs)
	}
}

func TestModuleGenerationUnavailable(t *testing.T) {
	t.Parallel()
	m := testModule(t, &Section{Enabled: true}, Deps{}, nil)
	for _, tool := range []string{ToolImageGenerate, ToolVideoGenerate} {
		_, err := m.Invoke(context.Background(), tool, json.RawMessage(`{"prompt":"x"}`), host.Env{})
		var he *host.Error
		if !errors.As(err, &he) || he.Code != "module_unavailable" {
			t.Fatalf("%s = %v, want module_unavailable", tool, err)
		}
	}
}

func TestModuleImageCapBeforeVendor(t *testing.T) {
	t.Parallel()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer srv.Close()
	store := newMemStore()
	today := time.Now().Format("2006-01-02")
	if err := store.Put(context.Background(), []byte("spend/day/"+today), []byte("999999")); err != nil {
		t.Fatal(err)
	}
	m := testModule(t, &Section{
		Enabled: true, ImageBackend: "openai", ImageEndpoint: srv.URL, ImageKeyEnv: "MEDIA_KEY",
		SessionMicros: 10,
	}, Deps{}, store)
	_, err := m.Invoke(context.Background(), ToolImageGenerate, json.RawMessage(`{"prompt":"x","size":"1024x1024"}`), host.Env{CallID: "c1"})
	var he *host.Error
	if !errors.As(err, &he) || he.Code != "budget_exhausted" {
		t.Fatalf("err = %v, want budget_exhausted", err)
	}
	if calls != 0 {
		t.Fatalf("vendor calls = %d, want 0", calls)
	}
}

func TestModuleImageGenerate(t *testing.T) {
	t.Parallel()
	png := base64.StdEncoding.EncodeToString([]byte("png"))
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if _, werr := w.Write([]byte(`{"data":[{"b64_json":"` + png + `"}]}`)); werr != nil {
			t.Errorf("vendor write: %v", werr)
		}
	}))
	defer srv.Close()
	m := testModule(t, &Section{
		Enabled: true, ImageBackend: "openai", ImageEndpoint: srv.URL, ImageKeyEnv: "MEDIA_KEY",
	}, Deps{}, newMemStore())
	res, err := m.Invoke(context.Background(), ToolImageGenerate, json.RawMessage(`{"prompt":"cat","size":"1024x1024"}`), host.Env{CallID: "c1"})
	if err != nil {
		t.Fatalf("image: %v", err)
	}
	if calls != 1 || res.UntrustedSource != "media:image/openai" || res.Meta["backend"] != "openai" {
		t.Fatalf("result = %+v, calls = %d", res, calls)
	}
}

type fakeHost struct{ store host.Store }

func (fakeHost) Emit(context.Context, host.Event) error { return nil }
func (fakeHost) I18n() host.Translator                  { return nil }
func (fakeHost) Spawn(context.Context, host.SpawnSpec) (host.Process, error) {
	return nil, errors.New("no")
}
func (fakeHost) LLM(string) host.Completer            { return nil }
func (h fakeHost) Storage(string) (host.Store, error) { return h.store, nil }

func TestNewFromConfig(t *testing.T) {
	t.Parallel()
	store := newMemStore()
	h := fakeHost{store: store}
	raw := json.RawMessage(`{"enabled":true,"image_backend":"openai","image_key_env":"MEDIA_KEY","session_micros":10,"daily_micros":20}`)
	m, err := NewFromConfig(h, raw, Deps{})
	if err != nil || m.store == nil || !m.cfg.Enabled {
		t.Fatalf("NewFromConfig = %+v, %v", m, err)
	}
	for _, bad := range []json.RawMessage{nil, json.RawMessage(`{`), json.RawMessage(`{"bogus":1}`), json.RawMessage(`{"image_endpoint":"ftp://x"}`)} {
		if _, err := NewFromConfig(h, bad, Deps{}); err == nil {
			t.Errorf("NewFromConfig(%q) succeeded", bad)
		}
	}
}

func TestModuleLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m := testModule(t, &Section{Enabled: true}, Deps{}, nil)
	if m.Status() != host.StatusReady {
		t.Fatalf("status = %q", m.Status())
	}
	if err := m.Stop(ctx); err != nil || m.Status() != host.StatusStopped {
		t.Fatalf("stop = %v, status %q", err, m.Status())
	}
	off := testModule(t, &Section{}, Deps{}, nil)
	if _, err := off.Invoke(ctx, ToolDocRead, json.RawMessage(`{"path":"x"}`), host.Env{}); err == nil {
		t.Fatal("disabled module served a call")
	}
}

func TestModuleDocReadErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m := testModule(t, &Section{Enabled: true}, Deps{}, nil)
	if _, err := m.Invoke(context.Background(), ToolDocRead, json.RawMessage(`{"path":"missing.txt"}`), host.Env{Cwd: dir}); err == nil {
		t.Fatal("missing file succeeded")
	}
	if _, err := m.Invoke(context.Background(), ToolDocRead, json.RawMessage(`{"path":"../x"}`), host.Env{Cwd: dir}); err == nil {
		t.Fatal("escape succeeded")
	}
}

func TestModuleVideoCapBeforeVendor(t *testing.T) {
	t.Parallel()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer srv.Close()
	m := testModule(t, &Section{
		Enabled: true, VideoBackend: "fal", VideoKeyEnv: "MEDIA_KEY",
		VideoCreate: srv.URL + "/create", VideoStatus: srv.URL + "/v/%s", DailyMicros: 1,
	}, Deps{}, newMemStore())
	_, err := m.Invoke(context.Background(), ToolVideoGenerate, json.RawMessage(`{"prompt":"x","duration_s":4}`), host.Env{CallID: "v"})
	var he *host.Error
	if !errors.As(err, &he) || he.Code != "budget_exhausted" || calls != 0 {
		t.Fatalf("err = %v, calls = %d", err, calls)
	}
}

func TestModuleVideoGenerate(t *testing.T) {
	t.Parallel()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method == http.MethodPost {
			if _, werr := w.Write([]byte(`{"id":"job1"}`)); werr != nil {
				t.Errorf("create: %v", werr)
			}
			return
		}
		if _, werr := w.Write([]byte(`{"status":"completed","url":"https://cdn.example/v.mp4"}`)); werr != nil {
			t.Errorf("status: %v", werr)
		}
	}))
	defer srv.Close()
	m := testModule(t, &Section{
		Enabled: true, VideoBackend: "fal", VideoKeyEnv: "MEDIA_KEY",
		VideoCreate: srv.URL + "/create", VideoStatus: srv.URL + "/v/%s",
	}, Deps{}, newMemStore())
	res, err := m.Invoke(context.Background(), ToolVideoGenerate, json.RawMessage(`{"prompt":"x","duration_s":4}`), host.Env{CallID: "v"})
	if err != nil || res.UntrustedSource != "media:video/fal" || res.Meta["backend"] != "fal" || calls < 2 {
		t.Fatalf("video = %+v, err %v, calls %d", res, err, calls)
	}
}

func TestModuleVideoUnavailableWithoutKey(t *testing.T) {
	t.Parallel()
	m := testModule(t, &Section{
		Enabled: true, VideoBackend: "fal", VideoKeyEnv: "MISSING",
		VideoCreate: "http://127.0.0.1:9/c", VideoStatus: "http://127.0.0.1:9/v/%s",
	}, Deps{Secrets: fakeSecrets{}}, nil)
	_, err := m.Invoke(context.Background(), ToolVideoGenerate, json.RawMessage(`{"prompt":"x"}`), host.Env{})
	var he *host.Error
	if !errors.As(err, &he) || he.Code != "module_unavailable" {
		t.Fatalf("err = %v", err)
	}
}

// TestMediaBudgetErrorPayload pins how the typed tool-budget denial becomes
// a tool-result payload: the closed code stays budget_exhausted, and the
// message carries `budget_tools`, the period, the numbers, and the command
// that raises the cap so the model never has to guess.
func TestMediaBudgetErrorPayload(t *testing.T) {
	t.Parallel()
	he := genError(&gen.BudgetError{
		Period: "daily", ProjectedMicros: 1_040_000,
		LimitMicros: 1_000_000, Raise: "damnbot usage budget set tools_daily_usd 15",
	})
	var hostErr *host.Error
	if !errors.As(he, &hostErr) || hostErr.Code != "budget_exhausted" {
		t.Fatalf("payload = %v", he)
	}
	for _, want := range []string{"budget_tools", "tools_daily", "1040000", "1000000", "damnbot usage budget set tools_daily_usd 15"} {
		if !strings.Contains(hostErr.Message, want) {
			t.Errorf("message %q lacks %q", hostErr.Message, want)
		}
	}
	if !strings.Contains(hostErr.Hint, "tools_session_usd") || !strings.Contains(hostErr.Hint, "tools_daily_usd") {
		t.Fatalf("hint = %q", hostErr.Hint)
	}
}
