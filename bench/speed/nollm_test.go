package speed

import (
	"encoding/json"
	"os"
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
	// Peta dimensi → fase disematkan dari tabel ADR-065/083 (NEW2-07): menambah, menghapus,
	// mengganti nama, atau memindah fase satu dimensi wajib mengubah ADR, artefak, dan peta ini.
	want := map[string]string{
		"Overhead agent per iterasi":                   "F1",
		"First token di UI (headless)":                 "F1",
		"First token di UI (TUI)":                      "F3",
		"Peak RSS":                                     "F4",
		"CPU idle":                                     "F4",
		"Exec spawn overhead":                          "F1",
		"Voice end-to-end":                             "F6",
		"Image preflight":                              "F6",
		"Crash/hang rate":                              "F4",
		"Pemulihan setelah kill -9":                    "F4",
		"Self-healing":                                 "F4",
		"Konkurensi":                                   "F2",
		"Proses yatim / perintah hang":                 "F1",
		"Edit salah lokasi":                            "F1",
		"Error tertelan (injeksi)":                     "F1",
		"Fallback tak terlihat":                        "F1",
		"Waktu install dari nol (VM bersih 10 Mbit/s)": "F9",
		"Error informatif":                             "F3",
		"Dukungan OS":                                  "F1",
		"Secret redaction":                             "F1",
		"Akses gateway orang asing":                    "F4",
		"Supply chain":                                 "F1",
		"Flake rate":                                   "F1",
		"Waktu CI PR":                                  "F1",
		"Kode mati":                                    "F1",
		"Waktu build bersih / inkremental":             "F3",
		"Token skill per turn":                         "F2",
	}
	got := map[string]string{}
	for _, d := range a.NotMeasured {
		if d.Reason == "" || got[d.Dimension] != "" {
			t.Errorf("entri not_measured tanpa alasan/duplikat: %+v", d)
		}
		got[d.Dimension] = d.Phase
	}
	for dim, p := range want {
		if got[dim] != p {
			t.Errorf("not_measured %q: fase %q, want %q (tabel ADR-065/083)", dim, got[dim], p)
		}
	}
	for dim := range got {
		if _, ok := want[dim]; !ok {
			t.Errorf("not_measured %q tidak ada di tabel ADR-065/083", dim)
		}
	}
	if len(a.Dimensions) == 0 {
		t.Error("dimensions F0 kosong")
	}
}
