package tts

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// speechServer answers one /audio/speech request and records its body.
type speechServer struct {
	*httptest.Server
	got  map[string]string
	auth string
}

// writeBody writes a fixture body and fails the test when the connection
// rejects it, so a broken fixture is never a silently empty response.
func writeBody(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	if _, err := w.Write([]byte(body)); err != nil {
		t.Errorf("write body: %v", err)
	}
}

func newSpeechServer(t *testing.T, fn func(w http.ResponseWriter, r *http.Request)) *speechServer {
	t.Helper()
	s := &speechServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.auth = r.Header.Get("Authorization")
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&s.got); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		fn(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func newBackend(t *testing.T, s *speechServer, cfg *Config) *HTTP {
	t.Helper()
	cfg.Endpoint = s.URL
	cfg.Doer = s.Client()
	h, err := NewHTTP(cfg)
	if err != nil {
		t.Fatalf("NewHTTP: %v", err)
	}
	return h
}

func TestHTTPSynthesize(t *testing.T) {
	t.Parallel()
	s := newSpeechServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		writeBody(t, w, "ID3fake-mp3")
	})
	h := newBackend(t, s, &Config{Key: "secret", Model: "tts-1", Voice: "nova"})
	audio, err := h.Synthesize(t.Context(), "halo dunia", Options{})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if audio.MIME != "audio/mpeg" || audio.Ext != ".mp3" || audio.Backend != "openai" {
		t.Errorf("audio = %+v", audio)
	}
	if s.auth != "Bearer secret" {
		t.Errorf("auth = %q", s.auth)
	}
	want := map[string]string{"input": "halo dunia", "model": "tts-1", "voice": "nova", "response_format": "mp3"}
	for k, v := range want {
		if s.got[k] != v {
			t.Errorf("payload[%s] = %q, want %q", k, s.got[k], v)
		}
	}
}

func TestHTTPSynthesizeHonestMIME(t *testing.T) {
	t.Parallel()
	s := newSpeechServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		writeBody(t, w, "RIFF\x00\x00\x00\x00WAVEfmt ")
	})
	h := newBackend(t, s, &Config{})
	audio, err := h.Synthesize(t.Context(), "halo", Options{})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if audio.MIME != "audio/wav" || audio.Ext != ".wav" {
		t.Errorf("audio = %+v, want sniffed WAV", audio)
	}
}

func TestHTTPSynthesizeOpusIsOgg(t *testing.T) {
	t.Parallel()
	s := newSpeechServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "audio/ogg")
		writeBody(t, w, "OggSopus")
	})
	h := newBackend(t, s, &Config{})
	audio, err := h.Synthesize(t.Context(), "halo", Options{Format: "opus"})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if audio.MIME != "audio/ogg" || audio.Ext != ".ogg" {
		t.Errorf("audio = %+v", audio)
	}
	if s.got["response_format"] != "opus" {
		t.Errorf("format = %q", s.got["response_format"])
	}
}

func TestHTTPRejectsBadInput(t *testing.T) {
	t.Parallel()
	s := newSpeechServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		writeBody(t, w, "ID3")
	})
	h := newBackend(t, s, &Config{})
	if _, err := h.Synthesize(t.Context(), "  ", Options{}); !errors.Is(err, ErrEmptyText) {
		t.Errorf("empty err = %v", err)
	}
	long := strings.Repeat("a", MaxTextBytes+1)
	if _, err := h.Synthesize(t.Context(), long, Options{}); !errors.Is(err, ErrTextTooLong) {
		t.Errorf("long err = %v", err)
	}
	if _, err := h.Synthesize(t.Context(), "halo", Options{Format: "flv"}); !errors.Is(err, ErrBadAudio) {
		t.Errorf("format err = %v", err)
	}
}

func TestHTTPStatusAndBodyFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		handler func(http.ResponseWriter, *http.Request)
		want    error
		cfg     Config
	}{
		{
			name: "status",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
				writeBody(t, w, "bad key")
			},
			want: ErrStatus,
			cfg:  Config{Key: "sk-do-not-leak"},
		},
		{
			name: "html",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				writeBody(t, w, "<html>nope</html>")
			},
			want: ErrBadAudio,
		},
		{
			name: "oversize",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "audio/mpeg")
				writeBody(t, w, "ID3-12345")
			},
			want: ErrTooLarge,
			cfg:  Config{MaxBytes: 4},
		},
		{
			name: "empty",
			handler: func(_ http.ResponseWriter, _ *http.Request) {
			},
			want: ErrBadAudio,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newSpeechServer(t, tc.handler)
			h := newBackend(t, s, &tc.cfg)
			_, err := h.Synthesize(t.Context(), "halo", Options{})
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if err != nil && strings.Contains(err.Error(), "sk-do-not-leak") {
				t.Errorf("error leaked the key: %v", err)
			}
		})
	}
}

func TestHTTPTimeout(t *testing.T) {
	t.Parallel()
	s := newSpeechServer(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	h := newBackend(t, s, &Config{})
	_, err := h.Synthesize(t.Context(), "halo", Options{Timeout: 50 * time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context deadline", err)
	}
}

// nopDoer satisfies the egress seam without constructing an http.Client.
type nopDoer struct{}

func (nopDoer) Do(*http.Request) (*http.Response, error) {
	return nil, errors.New("nop doer")
}

func TestNewHTTPConfig(t *testing.T) {
	t.Parallel()
	for _, cfg := range []Config{
		{Doer: nopDoer{}},
		{Endpoint: "ftp://example.com", Doer: nopDoer{}},
		{Endpoint: "https://", Doer: nopDoer{}},
		{Endpoint: "https://example.com"},
	} {
		if _, err := NewHTTP(&cfg); !errors.Is(err, ErrNotConfigured) {
			t.Errorf("NewHTTP(%+v) err = %v, want ErrNotConfigured", cfg, err)
		}
	}
	if _, err := NewHTTP(&Config{Endpoint: "https://example.com", Doer: nopDoer{}, Format: "bogus"}); !errors.Is(err, ErrBadAudio) {
		t.Errorf("format err = %v", err)
	}
}
