package stt

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// buildFixture compiles one cross-platform helper binary.
func buildFixture(t *testing.T, name, code string) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, name+".go")
	if err := os.WriteFile(src, []byte("package main\n"+code), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	bin := filepath.Join(dir, name)
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if out, err := exec.CommandContext(t.Context(), "go", "build", "-o", bin, src).CombinedOutput(); err != nil { // #nosec G204 -- fixed go tool
		t.Fatalf("build fixture: %v\n%s", err, out)
	}
	return bin
}

const whisperFixtureCode = `
import (
	"os"
)

func main() {
	out := ""
	for i := 1; i < len(os.Args); i++ {
		if os.Args[i] == "-of" && i+1 < len(os.Args) {
			out = os.Args[i+1]
		}
	}
	if out == "" {
		os.Exit(2)
	}
	body := []byte(` + "`" + `{"transcription":[{"offsets":{"from":0,"to":100},"text":" halo whisper"}]}` + "`" + `)
	if err := os.WriteFile(out+".json", body, 0o600); err != nil {
		os.Exit(3)
	}
}
`

const commandFixtureCode = `
import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		os.Exit(2)
	}
	raw, err := os.ReadFile(os.Args[1])
	if err != nil || len(raw) == 0 {
		os.Exit(3)
	}
	fmt.Print("halo dari command")
}
`

type transcribeRecorder struct {
	mu    sync.Mutex
	model string
	audio []byte
	auth  string
	code  int
	text  string
	url   string
	srv   *httptest.Server
}

func newTranscribeRecorder(t *testing.T, code int, text string) *transcribeRecorder {
	t.Helper()
	r := &transcribeRecorder{code: code, text: text}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.auth = req.Header.Get("Authorization")
		req.Body = http.MaxBytesReader(w, req.Body, 1<<20)
		mr, merr := req.MultipartReader()
		if merr != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		if perr := readParts(mr, r); perr != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		w.WriteHeader(r.code)
		if _, werr := fmt.Fprintf(w, `{"text":%q}`, r.text); werr != nil {
			t.Errorf("write response: %v", werr)
		}
	}))
	t.Cleanup(r.srv.Close)
	r.url = r.srv.URL
	return r
}

// readParts drains one multipart body into the recorder.
func readParts(mr *multipart.Reader, r *transcribeRecorder) error {
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("next part: %w", err)
		}
		raw, rerr := io.ReadAll(io.LimitReader(part, 1<<20))
		cerr := part.Close()
		if rerr != nil || cerr != nil {
			return fmt.Errorf("read part: %w", errors.Join(rerr, cerr))
		}
		switch part.FormName() {
		case "file":
			r.audio = raw
		case "model":
			r.model = string(raw)
		}
	}
}

// Do implements Doer with the recorder's loopback client.
func (r *transcribeRecorder) Do(req *http.Request) (*http.Response, error) {
	resp, err := r.srv.Client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("transcribe fixture: %w", err)
	}
	return resp, nil
}

// TestF6MEDIA010STTProviders invokes all eight registered providers: whisper.cpp,
// the OpenAI-compatible endpoint family, and the explicit command family.
func TestF6MEDIA010STTProviders(t *testing.T) {
	t.Parallel()
	ids := Catalog()
	if len(ids) != 8 {
		t.Fatalf("catalog = %v", ids)
	}
	whisper := buildFixture(t, "whisper", whisperFixtureCode)
	model := filepath.Join(t.TempDir(), "model.bin")
	if err := os.WriteFile(model, []byte("m"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := buildFixture(t, "sttcmd", commandFixtureCode)
	wav := wavBytes(t, 8000, 800)
	server := newTranscribeRecorder(t, http.StatusOK, "halo dari http")
	base := &Factory{WhisperBin: whisper, WhisperModel: model, Command: []string{command, "{audio}"}, Doer: server, Endpoint: server.url, Key: "sk-stt-secret"}
	for _, id := range ids {
		checkSTTProvider(t, id, base, wav)
	}
	if got := server.model; got == "" {
		t.Fatal("model field was not sent")
	}
	if got := server.auth; got != "Bearer sk-stt-secret" {
		t.Fatalf("auth = %q", got)
	}
	checkSTTExplicit(t)
	checkSTTNoLeak(t, wav)
}

func checkSTTProvider(t *testing.T, id string, base *Factory, wav []byte) {
	t.Helper()
	if _, ok := SpecOf(id); !ok {
		t.Fatalf("unknown spec %q", id)
	}
	f := *base
	p, err := f.New(id)
	if err != nil {
		t.Fatalf("New %s: %v", id, err)
	}
	tr, err := p.Transcribe(t.Context(), bytes.NewReader(wav), Options{})
	if err != nil {
		t.Fatalf("Transcribe %s: %v", id, err)
	}
	if tr.Backend != id || tr.Text == "" || tr.MIME != "audio/wav" {
		t.Fatalf("transcript %s = %+v", id, tr)
	}
}

func checkSTTExplicit(t *testing.T) {
	t.Helper()
	f := &Factory{}
	if _, err := f.New(""); err == nil {
		t.Fatal("empty backend selected a provider")
	}
	if _, err := f.New("nope"); err == nil {
		t.Fatal("unknown backend selected a provider")
	}
}

func checkSTTNoLeak(t *testing.T, wav []byte) {
	t.Helper()
	bad := newTranscribeRecorder(t, http.StatusUnauthorized, "")
	bf := &Factory{Endpoint: bad.url, Key: "sk-do-not-leak", Doer: bad}
	p, err := bf.New("openai")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = p.Transcribe(t.Context(), bytes.NewReader(wav), Options{})
	if err == nil {
		t.Fatal("401 accepted")
	}
	if strings.Contains(err.Error(), "sk-do-not-leak") || strings.Contains(err.Error(), string(wav[:4])) {
		t.Fatalf("error leaked key or audio: %v", err)
	}
}

// TestF6MEDIA010STTEmptyAudio fails closed on a zero-byte recording for both
// generic wires.
func TestF6MEDIA010STTEmptyAudio(t *testing.T) {
	t.Parallel()
	server := newTranscribeRecorder(t, http.StatusOK, "x")
	f := &Factory{Endpoint: server.url, Doer: server}
	p, err := f.New("groq")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, perr := p.Transcribe(t.Context(), bytes.NewReader(nil), Options{}); !errors.Is(perr, ErrEmptyAudio) {
		t.Fatalf("http empty err = %v", perr)
	}
	command := buildFixture(t, "sttcmd", commandFixtureCode)
	cf := &Factory{Command: []string{command, "{audio}"}}
	cp, err := cf.New("local_command")
	if err != nil {
		t.Fatalf("New command: %v", err)
	}
	if _, perr := cp.Transcribe(t.Context(), bytes.NewReader(nil), Options{}); !errors.Is(perr, ErrEmptyAudio) {
		t.Fatalf("command empty err = %v", perr)
	}
}
