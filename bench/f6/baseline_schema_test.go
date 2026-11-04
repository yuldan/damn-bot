package f6

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestF6BaselineArtifactSchema (ADR-323, baris "Baseline hermes F6"): the
// committed artefact must carry its provenance (machine, both commits, phase)
// and both F6 dimensions for both agents. The test also rejects an empty
// artefact and a `pass` verdict fabricated on a non-reference host, so the
// ledger row can close for "measured artefact" without claiming the 150 ms
// image target that ADR-323 keeps `N/A — hardware`.
func TestF6BaselineArtifactSchema(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../" + ArtifactPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 {
		t.Fatal("artefak kosong")
	}
	var art Artifact
	if err := json.Unmarshal(raw, &art); err != nil {
		t.Fatalf("artefak bukan JSON valid: %v", err)
	}

	required := map[string]string{
		"meta.phase":          art.Meta.Phase,
		"meta.hermes_commit":  art.Meta.HermesCommit,
		"meta.damnbot_commit": art.Meta.DamnbotCommit,
		"meta.machine":        art.Meta.Machine,
		"meta.harness":        art.Meta.Harness,
	}
	for field, value := range required {
		if strings.TrimSpace(value) == "" {
			t.Errorf("field wajib %s kosong", field)
		}
	}
	if art.Meta.HermesCommit != HermesCommit {
		t.Errorf("hermes_commit = %q, want %q", art.Meta.HermesCommit, HermesCommit)
	}
	if art.Meta.Phase != Phase {
		t.Errorf("phase = %q, want %q", art.Meta.Phase, Phase)
	}

	if len(art.Dimensions) != 2 {
		t.Fatalf("dimensions = %d, want 2 (voice_end_to_end, image_preflight)", len(art.Dimensions))
	}
	for _, name := range []string{"voice_end_to_end", "image_preflight"} {
		d, ok := art.Dimensions[name]
		if !ok {
			t.Errorf("dimensi wajib %s hilang", name)
			continue
		}
		if len(d.Target) == 0 {
			t.Errorf("%s: target kosong", name)
		}
		if d.Verdict == "" {
			t.Errorf("%s: verdict kosong", name)
		}
		for _, agent := range []string{"damnbot", "hermes"} {
			m, ok := d.Agents[agent]
			if !ok {
				t.Errorf("%s: agen %s hilang", name, agent)
				continue
			}
			if m.Status != "measured" {
				t.Errorf("%s/%s: status = %q, artefak final wajib measured", name, agent, m.Status)
			}
			if m.Runs <= 0 {
				t.Errorf("%s/%s: runs = %d, want > 0", name, agent, m.Runs)
			}
		}
	}
	if err := art.Validate(); err != nil {
		t.Errorf("artefak tidak valid: %v", err)
	}
	if image := art.Dimensions["image_preflight"]; image.Verdict == VerdictPass && art.Meta.Machine != ReferenceMachine {
		t.Errorf("image_preflight pass diklaim di %q; target hanya sah di %s", art.Meta.Machine, ReferenceMachine)
	}

	// Kontrol negatif: bentuk yang bisa memalsukan penutupan baris ditolak.
	var empty Artifact
	if err := empty.Validate(); err == nil {
		t.Error("artefak kosong diterima")
	}
	if err := json.Unmarshal([]byte(`{}`), &empty); err != nil {
		t.Fatalf("unmarshal objek kosong: %v", err)
	}
	if err := empty.Validate(); err == nil {
		t.Error("artefak `{}` diterima")
	}

	fake := Artifact{Meta: art.Meta, Dimensions: map[string]Dimension{}}
	for name, d := range art.Dimensions {
		fake.Dimensions[name] = d
	}
	image := fake.Dimensions["image_preflight"]
	image.Agents = map[string]Measurement{}
	for agent, m := range art.Dimensions["image_preflight"].Agents {
		m.Status, m.Machine, m.TotalMS = "measured", "workstation (not Q14)", 100
		image.Agents[agent] = m
	}
	image.Verdict = VerdictPass
	fake.Dimensions["image_preflight"] = image
	if err := fake.Validate(); err == nil {
		t.Error("verdict=pass palsu di host non-Q14 diterima")
	}
}
