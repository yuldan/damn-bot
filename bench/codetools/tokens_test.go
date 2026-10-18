package codetools

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/damnbot/damnbot/bench/budget/o200k"
)

// Setelan gate CT-11 (CODE-TOOLS §10.2; keputusan owner F1: dibandingkan pada limit yang sama
// dengan default `search_files` hermes = 50).
const (
	ct11MaxRatio  = 0.6
	ct11GrepLimit = 50 // 0 = default tool grep (100)
)

// TestGrepOutputTokens adalah CT-11: Σ token o200k_base output tool grep Damn-Bot untuk 50
// kueri golden (40 literal + 10 regex pertama queries.json) ≤ ct11MaxRatio × Σ golden hermes
// `search_files` yang direkam F0 di C1 (results/f0-baseline.json, ADR-077). Output Damn-Bot
// diambil lewat probe modul utama di C1 (DAMNBOT_CORPUS_C1 = checkout hermes 868b9f244f,
// `make corpora`); tanpa korpus test dilewati, kecuali DAMNBOT_REQUIRE_CORPORA=1 (job CI
// bench-codetools/bench-budget) yang membuatnya gagal.
func TestGrepOutputTokens(t *testing.T) {
	c1 := corpusC1(t)
	hermes, err := LoadHermes(filepath.Join("results", "f0-baseline.json"))
	if err != nil {
		t.Fatal(err)
	}
	golden := map[string]int{}
	theirs := 0
	for _, h := range hermes {
		if h.Kind == "search_files" {
			golden[h.ID] = h.Tokens
			theirs += h.Tokens
		}
	}
	if len(golden) != 50 {
		t.Fatalf("%d golden hermes, want 50", len(golden))
	}
	tok, err := o200k.Load()
	if err != nil {
		t.Fatal(err)
	}
	g, err := MeasureGrepTokens(filepath.Join("..", ".."), c1, ct11GrepLimit, tok)
	if err != nil {
		t.Fatal(err)
	}
	for id := range golden {
		if _, ok := g.PerOp[id]; !ok {
			t.Fatalf("probe tanpa op %s", id)
		}
	}
	ratio := float64(g.Tokens) / float64(theirs)
	t.Logf("CT-11 (limit %d): Σ token Damn-Bot %d (%d match, %.1f tok/match), hermes %d, rasio %.3f",
		ct11GrepLimit, g.Tokens, g.Matches, float64(g.Tokens)/float64(g.Matches), theirs, ratio)
	if ratio > ct11MaxRatio {
		t.Errorf("CT-11: rasio token %.3f > %.2f (Damn-Bot %d, hermes %d)", ratio, ct11MaxRatio, g.Tokens, theirs)
	}
}

// corpusC1 = DAMNBOT_CORPUS_C1; kosong → skip, atau gagal bila DAMNBOT_REQUIRE_CORPORA=1.
func corpusC1(t *testing.T) string {
	t.Helper()
	c1 := os.Getenv("DAMNBOT_CORPUS_C1")
	switch {
	case c1 != "":
		return c1
	case os.Getenv("DAMNBOT_REQUIRE_CORPORA") == "1":
		t.Fatal("DAMNBOT_CORPUS_C1 kosong padahal DAMNBOT_REQUIRE_CORPORA=1 (make corpora)")
	}
	t.Skip("DAMNBOT_CORPUS_C1 unset (make corpora, bench/corpora.lock)")
	return ""
}
