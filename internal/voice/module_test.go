//go:build !minimal

package voice

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/damnbot/damnbot/internal/modules/host"
)

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

func testModule(ctx context.Context, t *testing.T, s *Section, deps Deps) *Module {
	t.Helper()
	if deps.Approvals == nil {
		deps.Approvals = &fakeApprovals{answer: host.ApprovalAllowOnce}
	}
	m, err := New(s, deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		if err := m.Stop(ctx); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})
	return m
}

// fakeWhisper writes the -of JSON document with one transcription segment.
func fakeWhisper(t *testing.T) (bin, model string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "whisper")
	script := `#!/bin/sh
while [ $# -gt 0 ]; do
  case "$1" in
    -of) shift; out=$1 ;;
  esac
  shift
done
printf '%s' '{"transcription":[{"offsets":{"from":0,"to":100},"text":"halo dunia"}]}' > "$out.json"
`
	// #nosec G306 -- test fixture must be executable
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	model = filepath.Join(dir, "model.bin")
	if err := os.WriteFile(model, []byte("m"), 0o600); err != nil {
		t.Fatal(err)
	}
	return bin, model
}

func TestToolsMetadata(t *testing.T) {
	t.Parallel()
	tools := Tools()
	if len(tools) != 2 {
		t.Fatalf("Tools = %d, want 2", len(tools))
	}
	want := map[string]string{ToolTranscribe: "read", ToolSpeak: "non_idempotent"}
	for _, spec := range tools {
		if want[spec.Name] != spec.Effect || !json.Valid(spec.Schema) || spec.Description == "" {
			t.Fatalf("spec = %+v", spec)
		}
	}
}

func TestNewFromConfig(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`{"enabled":true,"stt_backend":"whispercpp","whisper_bin":"/bin/true","whisper_model":"/m"}`)
	m, err := NewFromConfig(raw, Deps{})
	if err != nil || !m.cfg.Enabled || m.cfg.STTBackend != "whispercpp" {
		t.Fatalf("NewFromConfig = %+v, %v", m, err)
	}
	for _, bad := range []json.RawMessage{
		nil, json.RawMessage(`{`), json.RawMessage(`{"bogus":1}`),
		json.RawMessage(`{"stt_backend":"nope"}`), json.RawMessage(`{"tts_backend":"nope"}`),
	} {
		if _, err := NewFromConfig(bad, Deps{}); err == nil {
			t.Errorf("NewFromConfig(%q) succeeded", bad)
		}
	}
}

func TestTranscribe(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bin, model := fakeWhisper(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.wav"), []byte("RIFF0000WAVE"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := testModule(ctx, t, &Section{
		Enabled: true, STTBackend: "whispercpp", WhisperBin: bin, WhisperModel: model,
	}, Deps{})
	res, err := m.Invoke(ctx, ToolTranscribe, json.RawMessage(`{"path":"a.wav"}`), host.Env{Cwd: dir})
	if err != nil || !strings.Contains(res.Content, "halo dunia") || res.UntrustedSource != "voice:transcribe" {
		t.Fatalf("transcribe = %+v, %v", res, err)
	}
}

func TestTranscribeUnavailable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m := testModule(ctx, t, &Section{Enabled: true, STTBackend: "whispercpp", WhisperBin: "/no/such/bin", WhisperModel: "/no/model"}, Deps{})
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.wav"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := m.Invoke(ctx, ToolTranscribe, json.RawMessage(`{"path":"a.wav"}`), host.Env{Cwd: dir})
	var he *host.Error
	if !errors.As(err, &he) || he.Code != "module_unavailable" {
		t.Fatalf("err = %v", err)
	}
	if _, err := m.Invoke(ctx, ToolTranscribe, json.RawMessage(`{"path":"missing.wav"}`), host.Env{Cwd: dir}); err == nil {
		t.Fatal("missing file succeeded")
	}
}

func TestSpeakApproval(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if _, err := w.Write([]byte("ID3audio")); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	defer srv.Close()
	for _, tc := range []struct {
		name   string
		answer host.Approval
		want   int
	}{
		{"denied", host.ApprovalDeny, 0},
		{"allow once", host.ApprovalAllowOnce, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls = 0
			appr := &fakeApprovals{answer: tc.answer}
			m := testModule(ctx, t, &Section{
				Enabled: true, TTSBackend: "openai", TTSEndpoint: srv.URL,
			}, Deps{Approvals: appr, Secrets: fakeSecrets{}})
			long := strings.Repeat("kata ", 60)
			res, err := m.Invoke(ctx, ToolSpeak, json.RawMessage(`{"text":"`+long+`"}`), host.Env{CallID: "s1"})
			if tc.want == 0 {
				var he *host.Error
				if !errors.As(err, &he) || he.Code != "denied" {
					t.Fatalf("err = %v", err)
				}
			} else if err != nil || res.UntrustedSource != "voice:speak" || !strings.Contains(res.Content, "audio") {
				t.Fatalf("speak = %+v, %v", res, err)
			}
			if calls != tc.want {
				t.Fatalf("TTS calls = %d, want %d", calls, tc.want)
			}
			reqs := appr.reqs
			if len(reqs) != 1 || len([]rune(reqs[0].Preview)) > 130 || strings.Count(reqs[0].Preview, "kata") > 25 {
				t.Fatalf("preview not clipped: %+v", reqs)
			}
		})
	}
}

func TestDisabledAndUnavailable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	off := testModule(ctx, t, &Section{}, Deps{})
	if off.Status() != host.StatusStopped {
		t.Fatalf("status = %q", off.Status())
	}
	for _, tool := range []string{ToolTranscribe, ToolSpeak} {
		if _, err := off.Invoke(ctx, tool, json.RawMessage(`{}`), host.Env{}); err == nil {
			t.Fatalf("%s served while disabled", tool)
		}
	}
	on := testModule(ctx, t, &Section{Enabled: true}, Deps{})
	if on.Status() != host.StatusReady {
		t.Fatalf("status = %q", on.Status())
	}
	for _, tool := range []string{ToolTranscribe, ToolSpeak} {
		var he *host.Error
		if _, err := on.Invoke(ctx, tool, json.RawMessage(`{"path":"a","text":"a"}`), host.Env{}); !errors.As(err, &he) || he.Code != "module_unavailable" {
			t.Fatalf("%s err = %v", tool, err)
		}
	}
}
