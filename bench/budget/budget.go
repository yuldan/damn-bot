// Package budget adalah harness token P1 (ROADMAP F0 #1, SPEC P1/§5.3): mengukur prompt per tier
// dan tool schema dalam tiga satuan — byte UTF-8, karakter (satuan kolom "Bytes" BASELINE-hermes.md,
// = Python len(str)), dan token o200k_base — plus estimasi hermes ceil(char/4)
// (agent/model_metadata.py CHARS_PER_TOKEN = 4) agar angka BASELINE dapat direproduksi.
package budget

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/damnbot/damnbot/bench/budget/o200k"
)

// charsPerToken = konversi hermes (BASELINE-hermes.md §intro).
const charsPerToken = 4

// Size = ukuran satu teks dalam semua satuan harness.
type Size struct {
	Bytes     int `json:"bytes"`
	Chars     int `json:"chars"`
	EstTokens int `json:"est_tokens_char4"` // ceil(chars/4), metode BASELINE
	O200k     int `json:"o200k_tokens"`
}

// Measure mengukur text.
func Measure(tok *o200k.Tokenizer, text string) Size {
	chars := utf8.RuneCountInString(text)
	return Size{
		Bytes:     len(text),
		Chars:     chars,
		EstTokens: ceilDiv(chars, charsPerToken),
		O200k:     tok.Count(text),
	}
}

func ceilDiv(a, b int) int { return (a + b - 1) / b }

// Snapshot = hasil ukur satu ekstraksi hermes (output bench/budget/hermes_extract.py).
type Snapshot struct {
	Stable       Size            `json:"stable"`
	Context      Size            `json:"context"`
	Volatile     Size            `json:"volatile"`
	SystemPrompt Size            `json:"system_prompt"`
	Schema       Size            `json:"schema"`       // tools_defs.json (metode BASELINE §7)
	SchemaWire   Size            `json:"schema_agent"` // agent.tools (yang dikirim AIAgent)
	SchemaCount  int             `json:"schema_count"`
	PerTool      map[string]Size `json:"per_tool"` // json.dumps per definisi (BASELINE "Schema terbesar")
	// Overhead per call = system prompt + schema.
	OverheadEst   int `json:"overhead_est_tokens_char4"`
	OverheadO200k int `json:"overhead_o200k_tokens"`
}

// toolDef cukup untuk mengambil nama; isi asli diukur sebagai byte mentah.
type toolDef struct {
	Function struct {
		Name string `json:"name"`
	} `json:"function"`
}

// LoadSnapshot membaca direktori ekstraksi dan mengukurnya.
func LoadSnapshot(tok *o200k.Tokenizer, dir string) (Snapshot, error) {
	read := func(name string) (string, error) {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return "", fmt.Errorf("budget: %w", err)
		}
		return string(b), nil
	}
	var s Snapshot
	texts := map[string]*Size{
		"stable.txt": &s.Stable, "context.txt": &s.Context, "volatile.txt": &s.Volatile,
		"system_prompt.txt": &s.SystemPrompt, "tools_defs.json": &s.Schema, "tools_agent.json": &s.SchemaWire,
	}
	for name, dst := range texts {
		text, err := read(name)
		if err != nil {
			return Snapshot{}, err
		}
		*dst = Measure(tok, text)
	}
	defs, err := read("tools_defs.json")
	if err != nil {
		return Snapshot{}, err
	}
	perTool, err := measurePerTool(tok, defs)
	if err != nil {
		return Snapshot{}, err
	}
	s.PerTool, s.SchemaCount = perTool, len(perTool)
	s.OverheadEst = s.SystemPrompt.EstTokens + s.Schema.EstTokens
	s.OverheadO200k = s.SystemPrompt.O200k + s.Schema.O200k
	return s, nil
}

// measurePerTool mengukur setiap definisi persis seperti `json.dumps(d)` Python per elemen:
// elemen array tools_defs.json ditulis Python dengan separator default yang sama, jadi
// json.RawMessage elemen = byte json.dumps elemen itu.
func measurePerTool(tok *o200k.Tokenizer, defs string) (map[string]Size, error) {
	var raws []json.RawMessage
	if err := json.Unmarshal([]byte(defs), &raws); err != nil {
		return nil, fmt.Errorf("budget: tools_defs.json: %w", err)
	}
	out := make(map[string]Size, len(raws))
	for _, raw := range raws {
		var d toolDef
		if err := json.Unmarshal(raw, &d); err != nil {
			return nil, fmt.Errorf("budget: tool def: %w", err)
		}
		out[d.Function.Name] = Measure(tok, string(raw))
	}
	return out, nil
}
