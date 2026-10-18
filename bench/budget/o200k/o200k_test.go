package o200k

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

type golden struct {
	Tiktoken string `json:"tiktoken"`
	Strings  []struct {
		Text string `json:"text"`
		IDs  []int  `json:"ids"`
	} `json:"strings"`
	Files []struct {
		Path  string `json:"path"`
		Count int    `json:"count"`
	} `json:"files"`
}

func loadGolden(t *testing.T) golden {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "golden.json"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var g golden
	if err := json.Unmarshal(data, &g); err != nil {
		t.Fatalf("parse golden: %v", err)
	}
	return g
}

func mustLoad(t *testing.T) *Tokenizer {
	t.Helper()
	tok, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return tok
}

// TestEncodeMatchesTiktoken: id token identik dengan tiktoken untuk string sulit.
func TestEncodeMatchesTiktoken(t *testing.T) {
	t.Parallel()
	tok := mustLoad(t)
	g := loadGolden(t)
	for _, c := range g.Strings {
		got := tok.Encode(c.Text)
		if len(got) == 0 && len(c.IDs) == 0 {
			continue
		}
		if !slices.Equal(got, c.IDs) {
			t.Errorf("Encode(%q) = %v, tiktoken %s = %v", c.Text, got, g.Tiktoken, c.IDs)
		}
		if n := tok.Count(c.Text); n != len(c.IDs) {
			t.Errorf("Count(%q) = %d, want %d", c.Text, n, len(c.IDs))
		}
	}
}

// TestCountMatchesTiktokenFiles: jumlah token identik untuk prompt/schema hermes yang diekstrak.
func TestCountMatchesTiktokenFiles(t *testing.T) {
	t.Parallel()
	tok := mustLoad(t)
	g := loadGolden(t)
	if len(g.Files) == 0 {
		t.Fatal("golden tanpa file")
	}
	for _, f := range g.Files {
		data, err := os.ReadFile(filepath.Join("..", f.Path))
		if err != nil {
			t.Fatalf("read %s: %v", f.Path, err)
		}
		if got := tok.Count(string(data)); got != f.Count {
			t.Errorf("%s: Count = %d, tiktoken = %d", f.Path, got, f.Count)
		}
	}
}

func TestParseRejectsCorruptRanks(t *testing.T) {
	t.Parallel()
	if _, err := parse([]byte("bukan gzip")); err == nil {
		t.Fatal("parse(bukan gzip) = nil error")
	}
}

func BenchmarkCount(b *testing.B) {
	tok, err := Load()
	if err != nil {
		b.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join("..", "testdata", "hermes-868b9f244f", "default", "system_prompt.txt"))
	if err != nil {
		b.Fatal(err)
	}
	text := string(data)
	b.SetBytes(int64(len(text)))
	for b.Loop() {
		tok.Count(text)
	}
}
