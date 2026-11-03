package voice

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestF6MEDIA010STTProviders(t *testing.T) {
	t.Parallel()
	want := "whispercpp,local_command,groq,openai,mistral,xai,elevenlabs,deepinfra"
	if got := strings.Join(Catalog(KindSTT), ","); got != want {
		t.Fatalf("stt catalog = %s, want %s", got, want)
	}
	if len(Catalog(KindSTT)) != 8 {
		t.Fatalf("stt providers = %d, want 8", len(Catalog(KindSTT)))
	}
}

func TestF6MEDIA007TTSProviders(t *testing.T) {
	t.Parallel()
	want := "edge,elevenlabs,openai,minimax,xai,mistral,gemini,neutts,kittentts,piper,deepinfra"
	if got := strings.Join(Catalog(KindTTS), ","); got != want {
		t.Fatalf("tts catalog = %s, want %s", got, want)
	}
	if len(Catalog(KindTTS)) != 11 {
		t.Fatalf("tts providers = %d, want 11", len(Catalog(KindTTS)))
	}
	if Catalog(Kind("other")) != nil {
		t.Error("unknown kind must have no catalog")
	}
}

func TestProviderSelection(t *testing.T) {
	t.Parallel()
	bin, err := os.Executable()
	if err != nil {
		t.Fatalf("executable: %v", err)
	}
	model := filepath.Join(t.TempDir(), "model.bin")
	if err := os.WriteFile(model, []byte("model"), 0o600); err != nil {
		t.Fatalf("write model: %v", err)
	}
	tests := []struct {
		name string
		cfg  Config
		want error
	}{
		{"empty", Config{}, ErrUnknownProvider},
		{"unknown", Config{STTBackend: "nope"}, ErrUnknownProvider},
		{"catalog_only", Config{STTBackend: "openai"}, ErrModuleUnavailable},
		{"unconfigured", Config{STTBackend: "whispercpp"}, ErrModuleUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := tc.cfg.NewSTT(); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	cfg := Config{STTBackend: "whispercpp", WhisperBin: bin, WhisperModel: model}
	if _, err := cfg.NewSTT(); err != nil {
		t.Fatalf("configured STT: %v", err)
	}
}
