// Command damnbot-speed builds the Damn-Bot half of the P28 speed-parity
// artefact. It never copies a hermes number: measured rows come from probe
// files written by the real Damn-Bot surfaces (CLI cold start, gateway
// dispatch, in-process tools/assemble, TUI slash), and every row that has no
// Damn-Bot feature is published as not_measured with the parity reason.
//
//	cd bench && go run ./speed/cmd/damnbot-speed -probes /tmp/probes \
//	    -existing speed/results/damnbot-8fab87144e.json \
//	    -out speed/results/damnbot-$(git rev-parse --short HEAD).json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/damnbot/damnbot/bench/speed"
)

type probeResult struct {
	SamplesMS []float64 `json:"samples_ms"`
	Error     string    `json:"error,omitempty"`
	Source    string    `json:"source,omitempty"`
}

type probeFile struct {
	Results map[string]probeResult `json:"results"`
}

type report struct {
	Meta    map[string]any `json:"meta"`
	Summary map[string]int `json:"summary"`
	Entries []speed.Entry  `json:"entries"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "damnbot-speed:", err)
		os.Exit(1)
	}
}

//nolint:funlen // one linear merge keeps the provenance rules in one place.
func run() error {
	probes := flag.String("probes", "", "directory with probe JSON files")
	hermesPath := flag.String("hermes", "speed/results/hermes-868b9f244f.json", "hermes artefact")
	parity := flag.String("parity", "../docs/FEATURE-PARITY.md", "FEATURE-PARITY.md")
	existing := flag.String("existing", "speed/results/damnbot-8fab87144e.json", "committed entries to keep (TOOL-045..047)")
	out := flag.String("out", "speed/results/damnbot.json", "output artefact")
	commit := flag.String("commit", "", "Damn-Bot commit")
	flag.Parse()
	if *probes == "" || *commit == "" {
		return fmt.Errorf("butuh -probes dan -commit")
	}

	var h struct {
		Entries []speed.Entry `json:"entries"`
	}
	if err := read(*hermesPath, &h); err != nil {
		return err
	}
	var old struct {
		Entries []speed.Entry `json:"entries"`
	}
	if err := read(*existing, &old); err != nil {
		return err
	}
	kept := map[string]speed.Entry{}
	for _, e := range old.Entries {
		if e.Class == speed.ClassMeasured {
			kept[e.ID] = e
		}
	}
	measured, err := loadProbes(*probes)
	if err != nil {
		return err
	}
	rows, err := readParity(*parity)
	if err != nil {
		return err
	}
	status := map[string]string{}
	for _, r := range rows {
		status[r.ID] = r.Status
	}

	rep := report{Meta: map[string]any{
		"phase": "F10", "damnbot_commit": *commit,
		"harness": "bench/speed: CLI cold start (1 warm-up+5), in-process tools/assemble (1+20), gateway/TUI slash (1+20); no hermes number copied",
		"basis":   "FEATURE-PARITY rows with class=measured in the hermes artefact",
	}, Summary: map[string]int{}}
	for _, e := range h.Entries {
		if e.Class != speed.ClassMeasured {
			continue
		}
		if k, ok := kept[e.ID]; ok {
			e.Class, e.Op, e.Reason = speed.ClassMeasured, nil, ""
			e.Result, e.Source = k.Result, k.Source
			rep.Entries = append(rep.Entries, e)
			rep.Summary["measured"]++
			continue
		}
		p, probed := measured[e.ID]
		if probed && p.Error == "" && len(p.SamplesMS) > 0 {
			e.Class, e.Op, e.Reason = speed.ClassMeasured, nil, ""
			e.Result = &speed.Result{
				SamplesMS: p.SamplesMS,
				P50MS:     speed.Percentile(p.SamplesMS, 50),
				P95MS:     speed.Percentile(p.SamplesMS, 95),
			}
			e.Source = p.Source
			rep.Entries = append(rep.Entries, e)
			rep.Summary["measured"]++
			continue
		}
		e.Class, e.Op, e.Result = speed.ClassNotMeasured, nil, nil
		e.Source = ""
		switch {
		case !strings.HasPrefix(status[e.ID], "LULUS"):
			e.Reason = "fitur belum ada di Damn-Bot; parity dire-fase ke F10 (ADR-350): " + status[e.ID]
			rep.Summary["waiting_parity"]++
		case probed && p.Error != "":
			e.Reason = "fitur ada tetapi probe gagal: " + p.Error
			rep.Summary["probe_failed"]++
		default:
			e.Reason = "fitur ada tetapi probe tidak menghasilkan sampel"
			rep.Summary["probe_failed"]++
		}
		rep.Entries = append(rep.Entries, e)
	}
	if len(rep.Entries) != 163 {
		return fmt.Errorf("entri = %d, want 163 measured rows of the hermes artefact", len(rep.Entries))
	}
	sort.Slice(rep.Entries, func(i, j int) bool { return rep.Entries[i].ID < rep.Entries[j].ID })
	rep.Summary["total"] = len(rep.Entries)
	if err := write(*out, rep); err != nil {
		return err
	}
	fmt.Printf("wrote %s: %d measured, %d not_measured (%d waiting parity)\n",
		*out, rep.Summary["measured"], rep.Summary["total"]-rep.Summary["measured"], rep.Summary["waiting_parity"])
	return nil
}

func loadProbes(dir string) (map[string]probeResult, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("tidak ada probe di %s", dir)
	}
	out := map[string]probeResult{}
	for _, p := range matches {
		var f probeFile
		if err := read(p, &f); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		for id, r := range f.Results {
			if _, dup := out[id]; dup {
				return nil, fmt.Errorf("probe %s duplikat", id)
			}
			out[id] = r
		}
	}
	return out, nil
}

func readParity(path string) ([]speed.Row, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path repo
	if err != nil {
		return nil, fmt.Errorf("parity: %w", err)
	}
	return speed.ParseParity(string(data)), nil
}

func read(path string, v any) error {
	data, err := os.ReadFile(path) //nolint:gosec // artefak repo
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

func write(path string, v any) error {
	data, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644) //nolint:gosec // artefak repo
}
