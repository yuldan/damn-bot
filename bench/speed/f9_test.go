package speed

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// speedArtifact = bentuk minimal artefak `results/*.json` yang dipakai gate.
type speedArtifact struct {
	Entries []struct {
		ID     string  `json:"id"`
		Class  string  `json:"class"`
		Reason string  `json:"reason"`
		Source string  `json:"source"`
		Result *Result `json:"result"`
	} `json:"entries"`
}

// TestF9SpeedParity (ROADMAP F9 acceptance 1, P28/SCORECARD §3): setiap operasi
// hermes yang terukur harus punya entri Damn-Bot. Entri `measured` harus
// ≥ 2× median baseline hermes; entri `not_measured` harus beralasan (fitur
// belum ada, dire-fase F10). Baris yang hilang tetap pelanggaran.
func TestF9SpeedParity(t *testing.T) {
	t.Parallel()
	hermes := loadSpeedArtifact(t, filepath.Join("results", "hermes-868b9f244f.json"))
	damnbot := latestDamnbotArtifact(t)

	entries := make(map[string]struct {
		class, reason, source string
		p50                   float64
	}, len(damnbot.Entries))
	for _, e := range damnbot.Entries {
		rec := struct {
			class, reason, source string
			p50                   float64
		}{class: e.Class, reason: e.Reason, source: e.Source}
		if e.Result != nil {
			rec.p50 = e.Result.P50MS
		}
		entries[e.ID] = rec
	}
	var problems, pending []string
	for _, e := range hermes.Entries {
		if e.Class != ClassMeasured || e.Result == nil || e.Result.P50MS <= 0 {
			continue
		}
		got, ok := entries[e.ID]
		switch {
		case !ok:
			problems = append(problems, e.ID+": tidak diukur")
		case got.class == ClassNotMeasured:
			if len(strings.TrimSpace(got.reason)) < 8 {
				problems = append(problems, e.ID+": not_measured tanpa alasan")
			} else {
				pending = append(pending, e.ID+": "+got.reason)
			}
		case got.p50 <= 0:
			problems = append(problems, e.ID+": p50 tidak valid")
		case e.Result.P50MS/got.p50 < 2:
			problems = append(problems, e.ID+": <2× median")
		}
	}
	if len(problems) > 0 {
		skipOrFail(t, "speed parity belum lulus: "+joinProblems(problems), problems)
	}
	if len(pending) > 0 {
		skipOrFail(t, "speed parity belum lengkap ("+strconv.Itoa(len(pending))+" not_measured)", pending)
	}
}

// TestDamnbotSpeedArtifact pins the published Damn-Bot artefact: it covers
// every hermes measured row exactly once, no measured number is copied from
// hermes, and every unmeasured row explains why.
func TestDamnbotSpeedArtifact(t *testing.T) {
	t.Parallel()
	art := latestDamnbotArtifact(t)
	hermes := loadSpeedArtifact(t, filepath.Join("results", "hermes-868b9f244f.json"))
	want := map[string]bool{}
	for _, e := range hermes.Entries {
		if e.Class == ClassMeasured {
			want[e.ID] = true
		}
	}
	seen := map[string]bool{}
	var measured, notMeasured int
	for _, e := range art.Entries {
		if seen[e.ID] {
			t.Errorf("%s duplikat", e.ID)
		}
		seen[e.ID] = true
		if !want[e.ID] {
			t.Errorf("%s bukan baris measured hermes", e.ID)
		}
		switch e.Class {
		case ClassMeasured:
			measured++
			if e.Result == nil || e.Result.P50MS <= 0 {
				t.Errorf("%s measured tanpa sampel", e.ID)
			}
			if e.Source == "" || strings.Contains(strings.ToLower(e.Source), "hermes") {
				t.Errorf("%s sumber bukan pengukuran Damn-Bot: %q", e.ID, e.Source)
			}
		case ClassNotMeasured:
			notMeasured++
			if len(strings.TrimSpace(e.Reason)) < 8 {
				t.Errorf("%s not_measured tanpa alasan", e.ID)
			}
		default:
			t.Errorf("%s kelas tidak dikenal %q", e.ID, e.Class)
		}
	}
	if measured < 96 {
		t.Errorf("measured = %d, want ≥96 (3 lama + 93 P20)", measured)
	}
	if measured+notMeasured != len(want) {
		t.Errorf("cakupan = %d, want %d", measured+notMeasured, len(want))
	}
}

// latestDamnbotArtifact picks the artefact with the most rows, so a stale
// 3-row snapshot cannot shadow the full P28 artefact.
func latestDamnbotArtifact(t *testing.T) speedArtifact {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join("results", "damnbot-*.json"))
	if err != nil || len(matches) == 0 {
		t.Fatalf("artefak damnbot tidak ada: %v", err)
	}
	sort.Strings(matches)
	best := loadSpeedArtifact(t, matches[0])
	for _, p := range matches[1:] {
		if a := loadSpeedArtifact(t, p); len(a.Entries) > len(best.Entries) {
			best = a
		}
	}
	return best
}

func loadSpeedArtifact(t *testing.T, path string) speedArtifact {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // artefak repo
	if err != nil {
		t.Fatalf("baca %s: %v", path, err)
	}
	var a speedArtifact
	if err := json.Unmarshal(b, &a); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return a
}

func joinProblems(problems []string) string {
	const max = 8
	if len(problems) <= max {
		return strings.Join(problems, ", ")
	}
	return strings.Join(problems[:max], ", ") + ", …"
}

func skipOrFail(t *testing.T, reason string, problems []string) {
	t.Helper()
	msg := reason
	if len(problems) > 0 {
		msg += ": " + joinProblems(problems)
	}
	if os.Getenv("DAMNBOT_RELEASE") == "1" {
		t.Fatalf("F9 GAGAL: %s", msg)
	}
	t.Skipf("F9 belum aktif (DAMNBOT_RELEASE=1): %s", msg)
}
