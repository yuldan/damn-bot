// Package f6 assembles the F6 baseline artefact (ADR-083, ADR-309): the two
// dimensions SCORECARD marks F6, measured for Hermes and Damn-Bot by one
// harness. The harness never calls an LLM: STT, LLM and TTS are timed fakes,
// and the image dimension is local CPU work only.
package f6

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Phase is the acceptance phase that owns this artefact.
const Phase = "F6"

// HermesCommit is the measured Hermes checkout (ADR-083 naming).
const HermesCommit = "868b9f244f5aa3ad238e494048046fe4f29c28ba"

// Artifact paths are relative to the bench module root.
const ArtifactPath = "speed/results/hermes-868b9f244f-F6.json"

// Targets are SPEC §17 / SCORECARD §1.2 goals. They are deliberately not
// configurable: lowering one is a spec change, not a benchmark tweak.
const (
	ImageTargetMS         = 150.0
	VoiceOverheadTargetMS = 200.0
	TTSStartTargetMS      = 300.0
	ReferenceMachine      = "Q14"
)

// Verdict values. "pass" is only legal when every agent is measured and the
// image measurements were taken on the Q14 reference machine.
const (
	VerdictPass         = "pass"
	VerdictFail         = "fail"
	VerdictNotMeasured  = "not_measured"
	VerdictNotReference = "not_reference"
)

// Measurement is one agent's result for one dimension. Either every number
// for that dimension is present (status "measured") or a reason is (status
// "not_measured"); a half-filled row is rejected by Validate.
type Measurement struct {
	Status   string  `json:"status"`
	Reason   string  `json:"reason,omitempty"`
	Machine  string  `json:"machine,omitempty"`
	Runs     int     `json:"runs,omitempty"`
	DecodeMS float64 `json:"decode_ms,omitempty"`
	ResizeMS float64 `json:"resize_ms,omitempty"`
	EncodeMS float64 `json:"encode_ms,omitempty"`
	TotalMS  float64 `json:"total_ms,omitempty"`
	Overhead float64 `json:"overhead_ms,omitempty"`
	TTSStart float64 `json:"tts_start_ms,omitempty"`
}

// Dimension is one F6 dimension for both agents.
type Dimension struct {
	Target  map[string]float64     `json:"target"`
	Agents  map[string]Measurement `json:"agents"`
	Verdict string                 `json:"verdict"`
}

// Artifact is the committed comparative baseline.
type Artifact struct {
	Meta       Meta                 `json:"meta"`
	Dimensions map[string]Dimension `json:"dimensions"`
}

// Meta records provenance and the no-LLM property of the harness.
type Meta struct {
	Phase            string            `json:"phase"`
	Date             string            `json:"date"`
	HermesCommit     string            `json:"hermes_commit"`
	Harness          string            `json:"harness"`
	LLM              string            `json:"llm"`
	ReferenceMachine string            `json:"reference_machine"`
	Machine          string            `json:"machine"`
	CPU              string            `json:"cpu,omitempty"`
	Kernel           string            `json:"kernel,omitempty"`
	DamnbotCommit    string            `json:"damnbot_commit,omitempty"`
	DamnbotDirty     string            `json:"damnbot_dirty,omitempty"`
	Caveats          map[string]string `json:"caveats,omitempty"`
}

// New builds an artefact with the fixed target maps.
func New(meta Meta, voice, image map[string]Measurement) *Artifact {
	a := &Artifact{
		Meta: meta,
		Dimensions: map[string]Dimension{
			"voice_end_to_end": {
				Target: map[string]float64{"overhead_ms": VoiceOverheadTargetMS, "tts_start_ms": TTSStartTargetMS},
				Agents: voice,
			},
			"image_preflight": {
				Target: map[string]float64{"total_ms": ImageTargetMS},
				Agents: image,
			},
		},
	}
	a.SetVerdicts()
	return a
}

// SetVerdicts recomputes every verdict from the measurements and targets.
func (a *Artifact) SetVerdicts() {
	for name, d := range a.Dimensions {
		d.Verdict = verdict(name, d)
		a.Dimensions[name] = d
	}
}

func verdict(name string, d Dimension) string {
	damnbot, hb := d.Agents["damnbot"]
	hermes, hh := d.Agents["hermes"]
	if !hb || !hh {
		return VerdictNotMeasured
	}
	if damnbot.Status != "measured" || hermes.Status != "measured" {
		return VerdictNotMeasured
	}
	switch name {
	case "voice_end_to_end":
		if damnbot.Overhead > VoiceOverheadTargetMS || hermes.Overhead > VoiceOverheadTargetMS ||
			damnbot.TTSStart > TTSStartTargetMS || hermes.TTSStart > TTSStartTargetMS {
			return VerdictFail
		}
		return VerdictPass
	case "image_preflight":
		if damnbot.TotalMS > ImageTargetMS || hermes.TotalMS > ImageTargetMS {
			return VerdictFail
		}
		// The target is defined on Q14. A faster development host must not
		// turn an under-target number into a pass.
		if damnbot.Machine != ReferenceMachine || hermes.Machine != ReferenceMachine {
			return VerdictNotReference
		}
		return VerdictPass
	default:
		return VerdictNotMeasured
	}
}

// Validate rejects shapes that could fake a pass: wrong phase, missing or
// extra dimensions, altered targets, incomplete measured rows, and a pass
// verdict inconsistent with the numbers.
func (a *Artifact) Validate() error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	if a.Meta.Phase != Phase {
		add("phase = %q, want %q", a.Meta.Phase, Phase)
	}
	if !strings.HasPrefix(a.Meta.HermesCommit, "868b9f244f") {
		add("hermes_commit = %q, want 868b9f244f…", a.Meta.HermesCommit)
	}
	if a.Meta.ReferenceMachine != ReferenceMachine {
		add("reference_machine = %q, want %q", a.Meta.ReferenceMachine, ReferenceMachine)
	}
	if !strings.Contains(a.Meta.LLM, "0 calls") {
		add("llm = %q, want a 0-call harness statement", a.Meta.LLM)
	}
	if len(a.Meta.Caveats) == 0 {
		add("meta.caveats is empty: fake I/O and non-reference host must be stated")
	}
	wantTargets := map[string]map[string]float64{
		"voice_end_to_end": {"overhead_ms": VoiceOverheadTargetMS, "tts_start_ms": TTSStartTargetMS},
		"image_preflight":  {"total_ms": ImageTargetMS},
	}
	if len(a.Dimensions) != len(wantTargets) {
		add("dimensions = %v, want exactly %v", slices.Sorted(maps.Keys(a.Dimensions)), slices.Sorted(maps.Keys(wantTargets)))
	}
	for name, want := range wantTargets {
		d, ok := a.Dimensions[name]
		if !ok {
			add("missing dimension %s", name)
			continue
		}
		if !maps.Equal(d.Target, want) {
			add("%s target = %v, want %v (targets are never lowered)", name, d.Target, want)
		}
		for _, agent := range []string{"damnbot", "hermes"} {
			m, ok := d.Agents[agent]
			if !ok {
				add("%s: missing agent %s", name, agent)
				continue
			}
			if err := validateMeasurement(name, agent, m); err != nil {
				errs = append(errs, err)
			}
		}
		if got := verdict(name, d); got != d.Verdict {
			add("%s verdict = %q, want %q from the recorded numbers", name, d.Verdict, got)
		}
	}
	return errors.Join(errs...)
}

func validateMeasurement(dim, agent string, m Measurement) error {
	switch m.Status {
	case "measured":
		if m.Reason != "" {
			return fmt.Errorf("%s/%s: measured row carries a reason", dim, agent)
		}
		if m.Runs <= 0 {
			return fmt.Errorf("%s/%s: measured row needs runs > 0", dim, agent)
		}
		switch dim {
		case "image_preflight":
			if m.TotalMS <= 0 || m.DecodeMS <= 0 || m.ResizeMS <= 0 || m.EncodeMS <= 0 {
				return fmt.Errorf("%s/%s: incomplete image stages %+v", dim, agent, m)
			}
		case "voice_end_to_end":
			if m.Overhead < 0 || m.TTSStart < 0 {
				return fmt.Errorf("%s/%s: negative voice latency %+v", dim, agent, m)
			}
		}
	case "not_measured":
		if strings.TrimSpace(m.Reason) == "" {
			return fmt.Errorf("%s/%s: not_measured without a reason", dim, agent)
		}
	default:
		return fmt.Errorf("%s/%s: status %q is not measured/not_measured", dim, agent, m.Status)
	}
	return nil
}
