// Command damnbot-speed builds the Damn-Bot half of the speed-parity artefact
// from measurements that already use the hermes protocol. It never copies a
// hermes number: each entry names its committed Damn-Bot source, and op IDs
// without a same-protocol measurement stay absent (the gate reports them).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/damnbot/damnbot/bench/speed"
)

type report struct {
	Meta    map[string]string `json:"meta"`
	Entries []speed.Entry     `json:"entries"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "damnbot-speed:", err)
		os.Exit(1)
	}
}

type source struct {
	id, file, kind string
	median         float64
}

func run() error {
	hermesPath := flag.String("hermes", "speed/results/hermes-868b9f244f.json", "hermes artefact")
	codePath := flag.String("codetools", "codetools/results/f1-7d0f6bd.json", "Damn-Bot codetools artefact")
	out := flag.String("out", "speed/results/damnbot-8fab87144e.json", "output artefact")
	commit := flag.String("commit", "8fab87144e", "Damn-Bot commit")
	flag.Parse()

	var hermes struct {
		Entries []speed.Entry `json:"entries"`
	}
	if err := read(*hermesPath, &hermes); err != nil {
		return err
	}
	var code struct {
		Ops []struct {
			Kind  string  `json:"kind"`
			P50MS float64 `json:"p50_ms"`
		} `json:"ops"`
	}
	if err := read(*codePath, &code); err != nil {
		return err
	}
	medians := map[string]float64{}
	byKind := map[string][]float64{}
	for _, o := range code.Ops {
		byKind[o.Kind] = append(byKind[o.Kind], o.P50MS)
	}
	for kind, xs := range byKind {
		sort.Float64s(xs)
		medians[kind] = xs[len(xs)/2]
	}
	// Only IDs whose Damn-Bot source measured the same operation warm and
	// in-process (BenchmarkCodetools under the same warm page cache).
	src := map[string]source{
		"TOOL-045": {"TOOL-045", *codePath, "patch", medians["patch"]},
		"TOOL-046": {"TOOL-046", *codePath, "read_file", medians["read_file"]},
		"TOOL-047": {"TOOL-047", *codePath, "search_files", medians["search_files"]},
	}
	rep := report{Meta: map[string]string{
		"phase": "F9", "damnbot_commit": *commit,
		"harness": "bench/speed/cmd/damnbot-speed: existing same-protocol Damn-Bot measurements",
		"note":    "partial: only IDs with a committed Damn-Bot measurement; absent IDs are gate failures",
	}}
	for _, e := range hermes.Entries {
		s, ok := src[e.ID]
		if !ok {
			continue
		}
		e.Class = speed.ClassMeasured
		e.Reason = ""
		e.Op = nil
		e.Result = &speed.Result{P50MS: s.median}
		e.Source = fmt.Sprintf("%s#ops[kind=%s].p50_ms median", s.file, s.kind)
		rep.Entries = append(rep.Entries, e)
	}
	if len(rep.Entries) == 0 {
		return fmt.Errorf("tidak ada sumber yang cocok")
	}
	sort.Slice(rep.Entries, func(i, j int) bool { return rep.Entries[i].ID < rep.Entries[j].ID })
	data, err := json.MarshalIndent(rep, "", " ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o750); err != nil {
		return err
	}
	if err := os.WriteFile(*out, append(data, '\n'), 0o644); err != nil { //nolint:gosec // artefak repo
		return err
	}
	fmt.Printf("wrote %s: %d entries\n", *out, len(rep.Entries))
	return nil
}

func read(path string, v any) error {
	data, err := os.ReadFile(path) //nolint:gosec // artefak repo
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}
