package e2e

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

func writeWhisperFixture(t *testing.T) (bin, model string) {
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
printf '%s' '{"transcription":[{"offsets":{"from":0,"to":100},"text":"halo dari fixture"}]}' > "$out.json"
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

func writeVoiceConfig(t *testing.T, e *Env, extra string) {
	t.Helper()
	body := "[modules.voice]\nenabled = true\n" + extra
	if err := os.WriteFile(e.cfg, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func voiceRunArgs(e *Env, prompt string) []string {
	return []string{
		"run", "-p", prompt,
		"--model", modelRef, "--base-url", e.Server.URL, "--api-key", "e2e",
		"--data-dir", e.DataDir, "--config", e.cfg,
		"--approval", "auto", "--output", "json",
	}
}

func TestF6VoiceTranscribeE2E(t *testing.T) {
	t.Parallel()
	bin, model := writeWhisperFixture(t)
	e := NewTrusted(t,
		ToolCallReply("s1", "tool_search", `{"query":"transcribe"}`),
		ToolCallReply("t1", "tool_invoke", `{"name":"transcribe","arguments":{"path":"clip.wav"}}`),
		providertest.Text("done"),
	)
	if err := os.WriteFile(filepath.Join(e.Workdir, "clip.wav"), []byte("RIFF0000WAVE"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeVoiceConfig(t, e, "stt_backend = \"whispercpp\"\nwhisper_bin = \""+bin+"\"\nwhisper_model = \""+model+"\"\n")
	res := e.Exec(voiceRunArgs(e, "transcribe the clip")...)
	res.RequireOK(t)
	reqs := e.Requests()
	if len(reqs) != 3 {
		t.Fatalf("provider requests = %d, want 3\nstderr:\n%s", len(reqs), res.Stderr)
	}
	transcript := string(reqs[2])
	for _, want := range []string{"halo dari fixture", `untrusted source=\"voice:transcribe\"`} {
		if !strings.Contains(transcript, want) {
			t.Errorf("transcript lacks %q", want)
			logTranscript(t, reqs[2])
		}
	}
}

func TestF6VoiceSpeakApprovalE2E(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	tts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		if _, err := w.Write([]byte("ID3audio")); err != nil {
			t.Errorf("tts write: %v", err)
		}
	}))
	defer tts.Close()
	for _, tc := range []struct {
		name  string
		stdin bool
		calls int64
	}{
		{"no approver", false, 0},
		{"fake approver", true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls.Store(0)
			e := NewTrusted(t,
				ToolCallReply("s1", "tool_search", `{"query":"speak"}`),
				ToolCallReply("p1", "tool_invoke", `{"name":"speak","arguments":{"text":"halo dunia"}}`),
				providertest.Text("done"),
			)
			writeVoiceConfig(t, e, "tts_backend = \"openai\"\ntts_endpoint = \""+tts.URL+"\"\n")
			args := voiceRunArgs(e, "speak the greeting")
			var res Result
			if tc.stdin {
				res = e.ExecStdin("y\n", args...)
			} else {
				res = e.Exec(args...)
			}
			res.RequireOK(t)
			if got := calls.Load(); got != tc.calls {
				t.Fatalf("TTS calls = %d, want %d\nstderr:\n%s", got, tc.calls, res.Stderr)
			}
			last := string(e.Requests()[len(e.Requests())-1])
			if tc.calls == 0 && !strings.Contains(last, `\"code\":\"denied\"`) {
				t.Fatalf("denial missing:\n%.1200s", last)
			}
			if tc.calls > 0 && !strings.Contains(last, `untrusted source=\"voice:speak\"`) {
				t.Fatalf("speak taint missing:\n%.1200s", last)
			}
		})
	}
}

func TestF6VoiceDisabledE2E(t *testing.T) {
	t.Parallel()
	e := New(t,
		ToolCallReply("t1", "transcribe", `{"path":"x.wav"}`),
		providertest.Text("done"),
	)
	if err := os.WriteFile(e.cfg, []byte("[modules.voice]\nenabled = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := e.Exec(voiceRunArgs(e, "transcribe x")...)
	res.RequireOK(t)
	reqs := e.Requests()
	declared := webToolNames(t, reqs[0])
	for _, name := range []string{"tool_search", "tool_invoke", "transcribe", "speak"} {
		if declared[name] {
			t.Errorf("%s advertised with voice disabled: %v", name, declared)
		}
	}
	if !strings.Contains(string(reqs[1]), "unknown_tool") {
		t.Errorf("direct call not refused:\n%.1200s", reqs[1])
	}
}
