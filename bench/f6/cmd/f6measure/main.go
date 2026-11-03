// Command f6measure assembles bench/speed/results/hermes-868b9f244f-F6.json
// from the F6 harness outputs. It makes no LLM call and never invents a
// measurement: a missing input becomes an explicit not_measured row.
//
// Inputs are produced by bench/f6/run.sh:
//   - Go benchmark output for BenchmarkImagePreflightStages (Damn-Bot image)
//   - go test -v output for TestVoicePipelineLatency (Damn-Bot voice)
//   - JSON from hermes_image.py and hermes_voice.py (Hermes side)
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/damnbot/damnbot/bench/f6"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	imageBench := flag.String("damnbot-image", "", "go test -bench output for BenchmarkImagePreflightStages")
	voiceLog := flag.String("damnbot-voice", "", "go test -v output for TestVoicePipelineLatency")
	hermesImage := flag.String("hermes-image", "", "JSON from hermes_image.py")
	hermesVoice := flag.String("hermes-voice", "", "JSON from hermes_voice.py")
	out := flag.String("out", f6.ArtifactPath, "artefact path (bench-relative)")
	date := flag.String("date", time.Now().UTC().Format("2006-01-02"), "measurement date")
	hermesCommit := flag.String("hermes-commit", f6.HermesCommit, "measured Hermes commit")
	machine := flag.String("machine", "workstation (not Q14)", "machine that produced the numbers")
	cpu := flag.String("cpu", "", "CPU model")
	kernel := flag.String("kernel", "", "kernel")
	dbCommit := flag.String("damnbot-commit", "", "Damn-Bot commit under measurement")
	dirty := flag.String("damnbot-dirty", "0", "uncommitted paths")
	flag.Parse()

	image := map[string]f6.Measurement{
		"damnbot": readBench(*imageBench, *machine),
		"hermes":  readHermes(*hermesImage),
	}
	voice := map[string]f6.Measurement{
		"damnbot": readVoice(*voiceLog),
		"hermes":  readHermes(*hermesVoice),
	}
	art := f6.New(f6.Meta{
		Phase: f6.Phase, Date: *date, HermesCommit: *hermesCommit,
		Harness: "bench/f6/run.sh: BenchmarkImagePreflightStages + TestVoicePipelineLatency " +
			"(shared fake STT/LLM/TTS protocol) + hermes_image.py/hermes_voice.py",
		LLM:              "0 calls (fake STT/LLM/TTS and local image work; no provider traffic)",
		ReferenceMachine: f6.ReferenceMachine,
		Machine:          *machine, CPU: *cpu, Kernel: *kernel,
		DamnbotCommit: *dbCommit, DamnbotDirty: *dirty,
		Caveats: map[string]string{
			"voice":   "fake 10 ms TTS and timestamped fake LLM; playback is a no-op, so provider/audio-device latency is excluded by design",
			"image":   "4000x3000 JPEG with the 2048 px / 3.75 MB / q85 preset; Damn-Bot uses stdlib stages, Hermes uses Pillow stages",
			"verdict": "pass requires measured rows and all targets; image also requires machine Q14",
		},
	}, voice, image)
	if err := art.Validate(); err != nil {
		return fmt.Errorf("f6measure: %w", err)
	}
	raw, err := json.MarshalIndent(art, "", " ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(*out, append(raw, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s (voice %s, image %s)\n", *out,
		art.Dimensions["voice_end_to_end"].Verdict, art.Dimensions["image_preflight"].Verdict)
	return nil
}

var benchStageRe = regexp.MustCompile(`^BenchmarkImagePreflightStages/(decode|resize|encode|total)(?:-\d+)?$`)

// readBench parses the benchmark's per-stage ns/op lines. Any missing stage
// makes the Damn-Bot image row not_measured rather than partly measured.
func readBench(path, machine string) f6.Measurement {
	if path == "" {
		return notMeasured("image benchmark not supplied")
	}
	f, err := os.Open(path)
	if err != nil {
		return notMeasured("image benchmark: " + err.Error())
	}
	defer f.Close()
	stages := map[string]float64{}
	runs := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 {
			continue
		}
		m := benchStageRe.FindStringSubmatch(fields[0])
		if m == nil {
			continue
		}
		for i, field := range fields {
			if field != "ns/op" || i < 2 {
				continue
			}
			ns, err := strconv.ParseFloat(fields[i-1], 64)
			if err != nil {
				continue
			}
			stages[m[1]] = ns / 1e6
			if n, err := strconv.Atoi(fields[i-2]); err == nil {
				runs = max(runs, n)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return notMeasured("image benchmark: " + err.Error())
	}
	for _, stage := range []string{"decode", "resize", "encode", "total"} {
		if stages[stage] <= 0 {
			return notMeasured("image benchmark missing stage " + stage)
		}
	}
	return f6.Measurement{
		Status: "measured", Machine: machine, Runs: runs,
		DecodeMS: stages["decode"], ResizeMS: stages["resize"],
		EncodeMS: stages["encode"], TotalMS: stages["total"],
	}
}

// readVoice parses every machine-readable latency line and reports the median,
// so the Damn-Bot and Hermes rows use the same sample count.
func readVoice(path string) f6.Measurement {
	if path == "" {
		return notMeasured("voice test log not supplied")
	}
	f, err := os.Open(path)
	if err != nil {
		return notMeasured("voice test log: " + err.Error())
	}
	defer f.Close()
	const marker = "F6-VOICE-LATENCY "
	var overhead, ttsStart []float64
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		i := strings.Index(line, marker)
		if i < 0 {
			continue
		}
		var sample struct {
			OverheadMS float64 `json:"overhead_ms"`
			TTSStartMS float64 `json:"tts_start_ms"`
		}
		if err := json.Unmarshal([]byte(line[i+len(marker):]), &sample); err != nil {
			return notMeasured("voice line: " + err.Error())
		}
		overhead = append(overhead, sample.OverheadMS)
		ttsStart = append(ttsStart, sample.TTSStartMS)
	}
	if err := sc.Err(); err != nil {
		return notMeasured("voice test log: " + err.Error())
	}
	if len(overhead) == 0 {
		return notMeasured("voice test log has no " + strings.TrimSpace(marker) + " line")
	}
	return f6.Measurement{
		Status: "measured", Runs: len(overhead),
		Overhead: median(overhead), TTSStart: median(ttsStart),
	}
}

func median(values []float64) float64 {
	ordered := slices.Clone(values)
	slices.Sort(ordered)
	n := len(ordered)
	if n%2 == 1 {
		return ordered[n/2]
	}
	return (ordered[n/2-1] + ordered[n/2]) / 2
}

// readHermes accepts the sidecar's measured row or its honest not_measured row.
func readHermes(path string) f6.Measurement {
	if path == "" {
		return notMeasured("Hermes sidecar not supplied")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return notMeasured("Hermes sidecar: " + err.Error())
	}
	var m f6.Measurement
	if err := json.Unmarshal(raw, &m); err != nil {
		return notMeasured("Hermes sidecar: " + err.Error())
	}
	if m.Status == "" {
		m.Status = "measured"
	}
	return m
}

func notMeasured(reason string) f6.Measurement {
	return f6.Measurement{Status: "not_measured", Reason: reason}
}
