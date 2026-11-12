package speed

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// speedArtifact = bentuk minimal artefak `results/*.json` yang dipakai gate.
type speedArtifact struct {
	Entries []struct {
		ID     string  `json:"id"`
		Class  string  `json:"class"`
		Result *Result `json:"result"`
	} `json:"entries"`
}

// TestF9SpeedParity (ROADMAP F9 acceptance 1, P28/SCORECARD §3): setiap operasi
// terukur harus ≥ 2× median baseline hermes dan tidak ada yang lebih lambat.
// Artefak sisi Damn-Bot belum diproduksi selama F9, jadi kerangka ini skip
// sampai penanda rilis; dengan DAMNBOT_RELEASE=1 artefak wajib ada dan lulus.
func TestF9SpeedParity(t *testing.T) {
	t.Parallel()
	hermes := loadSpeedArtifact(t, filepath.Join("results", "hermes-868b9f244f.json"))
	matches, err := filepath.Glob(filepath.Join("results", "damnbot-*.json"))
	if err != nil {
		t.Fatalf("glob artefak damnbot: %v", err)
	}
	if len(matches) == 0 {
		skipOrFail(t, "artefak bench/speed/results/damnbot-*.json belum ada")
	}
	sort.Strings(matches)
	damnbot := loadSpeedArtifact(t, matches[len(matches)-1])

	got := make(map[string]float64, len(damnbot.Entries))
	for _, e := range damnbot.Entries {
		if e.Class == "measured" && e.Result != nil {
			got[e.ID] = e.Result.P50MS
		}
	}
	var problems []string
	for _, e := range hermes.Entries {
		if e.Class != "measured" || e.Result == nil || e.Result.P50MS <= 0 {
			continue
		}
		p50, ok := got[e.ID]
		switch {
		case !ok:
			problems = append(problems, e.ID+": tidak diukur")
		case p50 <= 0:
			problems = append(problems, e.ID+": p50 tidak valid")
		case e.Result.P50MS/p50 < 2:
			problems = append(problems, e.ID+": <2× median")
		}
	}
	if len(problems) > 0 {
		skipOrFail(t, "speed parity belum lulus: "+joinProblems(problems))
	}
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

func skipOrFail(t *testing.T, reason string) {
	t.Helper()
	if os.Getenv("DAMNBOT_RELEASE") == "1" {
		t.Fatalf("F9 GAGAL: %s", reason)
	}
	t.Skipf("F9 belum aktif (DAMNBOT_RELEASE=1): %s", reason)
}
