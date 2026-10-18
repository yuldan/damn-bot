package speed

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"
)

// TestNoLLMBaselineCoverage (ADR-065, ADR-083): artefak baseline hermes tanpa LLM membaca mesin
// daftar dimensi yang belum diukur di F0, masing-masing dengan fase F1..F9 dan alasan. Jumlahnya
// = baris berstatus Fx di tabel ADR-065/083; menambah/menghapus dimensi = ubah ADR + artefak.
func TestNoLLMBaselineCoverage(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile("results/hermes-868b9f244f-nollm.json")
	if err != nil {
		t.Fatal(err)
	}
	var a struct {
		Dimensions  map[string]json.RawMessage `json:"dimensions"`
		NotMeasured []struct {
			Dimension, Phase, Reason string
		} `json:"not_measured"`
	}
	if err := json.Unmarshal(b, &a); err != nil {
		t.Fatal(err)
	}
	const wantNotMeasured = 27
	if len(a.NotMeasured) != wantNotMeasured {
		t.Errorf("not_measured = %d entri, want %d (tabel ADR-065/083)", len(a.NotMeasured), wantNotMeasured)
	}
	phase := regexp.MustCompile(`^F[1-9]$`)
	seen := map[string]bool{}
	for _, d := range a.NotMeasured {
		if d.Dimension == "" || d.Reason == "" || !phase.MatchString(d.Phase) || seen[d.Dimension] {
			t.Errorf("entri not_measured tidak valid/duplikat: %+v", d)
		}
		seen[d.Dimension] = true
	}
	if len(a.Dimensions) == 0 {
		t.Error("dimensions F0 kosong")
	}
}
