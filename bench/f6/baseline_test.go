package f6

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestF6HermesBaseline is the acceptance gate for the F6 baseline artefact:
// both F6 dimensions exist for both agents, the targets are exactly the SPEC
// goals, and no row claims a pass it did not measure. The image target is
// defined on Q14, so a workstation run cannot report a pass even when the
// measured time is under 150 ms.
func TestF6HermesBaseline(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../" + ArtifactPath)
	if err != nil {
		t.Fatal(err)
	}
	var art Artifact
	if err := json.Unmarshal(raw, &art); err != nil {
		t.Fatal(err)
	}
	if err := art.Validate(); err != nil {
		t.Fatalf("artefact invalid: %v", err)
	}
	if art.Meta.Phase != Phase || !strings.HasPrefix(art.Meta.HermesCommit, "868b9f244f") {
		t.Fatalf("meta = %+v", art.Meta)
	}
	if got := art.Dimensions["image_preflight"].Target["total_ms"]; got != ImageTargetMS {
		t.Fatalf("image target = %v, want %v", got, ImageTargetMS)
	}
	if got := art.Dimensions["voice_end_to_end"].Target["overhead_ms"]; got != VoiceOverheadTargetMS {
		t.Fatalf("voice overhead target = %v, want %v", got, VoiceOverheadTargetMS)
	}
	if got := art.Dimensions["voice_end_to_end"].Target["tts_start_ms"]; got != TTSStartTargetMS {
		t.Fatalf("tts start target = %v, want %v", got, TTSStartTargetMS)
	}
	image := art.Dimensions["image_preflight"]
	if image.Verdict == VerdictPass && art.Meta.Machine != ReferenceMachine {
		t.Fatalf("image pass claimed on %q; target is measured on %s", art.Meta.Machine, ReferenceMachine)
	}
}

// TestF6BaselineVerdictRules locks the "never claim an unmeasured pass" rules.
func TestF6BaselineVerdictRules(t *testing.T) {
	t.Parallel()
	measured := Measurement{Status: "measured", Runs: 1}
	image := func(damnbot, hermes Measurement) Dimension {
		return Dimension{
			Target: map[string]float64{"total_ms": ImageTargetMS},
			Agents: map[string]Measurement{"damnbot": damnbot, "hermes": hermes},
		}
	}
	fast := measured
	fast.DecodeMS, fast.ResizeMS, fast.EncodeMS, fast.TotalMS = 1, 1, 1, 100
	slow := fast
	slow.TotalMS = ImageTargetMS + 1
	q14 := fast
	q14.Machine = ReferenceMachine
	notMeasured := Measurement{Status: "not_measured", Reason: "not run"}

	if got := verdict("image_preflight", image(notMeasured, q14)); got != VerdictNotMeasured {
		t.Fatalf("missing row verdict = %q", got)
	}
	if got := verdict("image_preflight", image(slow, q14)); got != VerdictFail {
		t.Fatalf("over-target verdict = %q", got)
	}
	if got := verdict("image_preflight", image(fast, fast)); got != VerdictNotReference {
		t.Fatalf("workstation verdict = %q", got)
	}
	if got := verdict("image_preflight", image(q14, q14)); got != VerdictPass {
		t.Fatalf("Q14 under-target verdict = %q", got)
	}
}
