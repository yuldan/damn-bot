package tts

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// buildTTSFixture compiles a tiny argv-driven provider: it verifies the text
// arrived as exactly one argument and writes the requested bytes.
func buildTTSFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "tts_fixture.go")
	code := `package main

import (
	"os"
	"time"
)

func main() {
	if len(os.Args) != 5 {
		os.Exit(2)
	}
	out, text, want, content := os.Args[1], os.Args[2], os.Args[3], os.Args[4]
	if text != want {
		os.Exit(3)
	}
	if content == "SLEEP" {
		time.Sleep(5 * time.Second)
	}
	if err := os.WriteFile(out, []byte(content), 0o600); err != nil {
		os.Exit(4)
	}
}
`
	if err := os.WriteFile(src, []byte(code), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	name := "ttsfix"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	bin := filepath.Join(dir, name)
	if out, err := exec.CommandContext(t.Context(), "go", "build", "-o", bin, src).CombinedOutput(); err != nil { // #nosec G204 -- fixed go tool
		t.Fatalf("build fixture: %v\n%s", err, out)
	}
	return bin
}

// speechRecorder is one fake OpenAI-compatible endpoint.
type speechRecorder struct {
	mu   sync.Mutex
	body map[string]string
	auth string
	code int
	raw  string
	url  string
	srv  *httptest.Server
}

func newSpeechRecorder(t *testing.T, code int, raw string) *speechRecorder {
	t.Helper()
	r := &speechRecorder{code: code, raw: raw}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.auth = req.Header.Get("Authorization")
		r.body = map[string]string{}
		if derr := json.NewDecoder(req.Body).Decode(&r.body); derr != nil {
			t.Errorf("decode request: %v", derr)
		}
		w.WriteHeader(r.code)
		if _, werr := w.Write([]byte(r.raw)); werr != nil {
			t.Errorf("write response: %v", werr)
		}
	}))
	t.Cleanup(srv.Close)
	r.url, r.srv = srv.URL, srv
	return r
}

// Client is the loopback client for the fake endpoint.
func (r *speechRecorder) Client() *http.Client { return r.srv.Client() }

func TestF6TOOL080TextToSpeech(t *testing.T) {
	t.Parallel()
	ids := Catalog()
	if len(ids) != 11 {
		t.Fatalf("catalog = %v", ids)
	}
	fixture := buildTTSFixture(t)
	payload := "halo; touch SHOULD_NOT_EXIST && echo $HOME"
	server := newSpeechRecorder(t, http.StatusOK, "ID3fake-mp3")
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	base := &Factory{Key: "sk-tool080", Command: []string{fixture, "{out}", "{text}", payload, "ID3fake-mp3"}, Doer: server.Client(), Voice: "alloy", Endpoint: server.url}
	for _, id := range ids {
		checkTTSProvider(t, id, base, payload, cwd)
	}
	checkTTSCommand(t, fixture)
	checkTTSOpus(t)
	checkTTSNoLeak(t)
}

func checkTTSProvider(t *testing.T, id string, src *Factory, payload, cwd string) {
	t.Helper()
	f := *src
	spec, ok := SpecOf(id)
	if !ok {
		t.Fatalf("unknown spec %q", id)
	}
	if spec.Wire != WireOpenAI {
		f.Endpoint = ""
	}
	p, err := f.New(id)
	if err != nil {
		t.Fatalf("New %s: %v", id, err)
	}
	audio, err := p.Synthesize(t.Context(), payload, Options{})
	if err != nil {
		t.Fatalf("Synthesize %s: %v", id, err)
	}
	if audio.Backend != id || audio.MIME != "audio/mpeg" || audio.Ext != ".mp3" {
		t.Fatalf("audio %s = %+v", id, audio)
	}
	if _, serr := os.Stat(filepath.Join(cwd, "SHOULD_NOT_EXIST")); serr == nil {
		t.Fatal("command text was interpreted by a shell")
	}
}

func checkTTSCommand(t *testing.T, fixture string) {
	t.Helper()
	f := &Factory{Command: []string{fixture, "{out}", "{text}", "slow", "SLEEP"}}
	p, err := f.New("piper")
	if err != nil {
		t.Fatalf("New piper: %v", err)
	}
	if _, terr := p.Synthesize(t.Context(), "slow", Options{Timeout: 50 * time.Millisecond}); !errors.Is(terr, ErrTimeout) {
		t.Fatalf("timeout err = %v", terr)
	}
	wavCmd := &Factory{Command: []string{fixture, "{out}", "{text}", "w", "RIFF0000WAVE"}}
	wavProvider, err := wavCmd.New("kittentts")
	if err != nil {
		t.Fatalf("New kittentts: %v", err)
	}
	audio, err := wavProvider.Synthesize(t.Context(), "w", Options{})
	if err != nil || audio.MIME != "audio/wav" {
		t.Fatalf("wav audio = %+v, err = %v", audio, err)
	}
}

func checkTTSOpus(t *testing.T) {
	t.Helper()
	ogg := newSpeechRecorder(t, http.StatusOK, "OggSopus")
	provider, err := (&Factory{Endpoint: ogg.url, Doer: ogg.Client()}).New("openai")
	if err != nil {
		t.Fatalf("New openai: %v", err)
	}
	audio, err := provider.Synthesize(t.Context(), "halo", Options{})
	if err != nil {
		t.Fatalf("Synthesize opus: %v", err)
	}
	if audio.MIME != "audio/ogg" || audio.Ext != ".ogg" {
		t.Fatalf("ogg audio = %+v", audio)
	}
	if got := ogg.lastFormat(); got != "opus" {
		t.Fatalf("default response_format = %q, want opus", got)
	}
}

func checkTTSNoLeak(t *testing.T) {
	t.Helper()
	bad := newSpeechRecorder(t, http.StatusUnauthorized, "bad key")
	provider, err := (&Factory{Endpoint: bad.url, Doer: bad.Client(), Key: "sk-do-not-leak"}).New("deepinfra")
	if err != nil {
		t.Fatalf("New deepinfra: %v", err)
	}
	_, err = provider.Synthesize(t.Context(), "halo", Options{})
	if !errors.Is(err, ErrStatus) {
		t.Fatalf("status err = %v", err)
	}
	if strings.Contains(err.Error(), "sk-do-not-leak") {
		t.Fatalf("error leaked the key: %v", err)
	}
}

func (r *speechRecorder) lastFormat() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body["response_format"]
}
