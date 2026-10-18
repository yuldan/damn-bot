// Command speed mengukur baseline speed parity hermes tanpa LLM/jaringan dan menulis artefak JSON.
//
//	cd bench && go run ./speed/cmd/speed -hermes ~/Dokumen/hermes-agent -out speed/results/hermes-868b9f244f.json
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/damnbot/damnbot/bench/speed"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type summary struct {
	Rows          int            `json:"rows"`
	ByClass       map[string]int `json:"by_class"`
	MeasuredProxy int            `json:"measured_help_proxy"`
	ErrorsToNM    int            `json:"errors_reclassified_not_measurable"`
}

type report struct {
	Meta    map[string]string `json:"meta"`
	Summary summary           `json:"summary"`
	Startup *speed.Result     `json:"startup_chat_pty"`
	Entries []speed.Entry     `json:"entries"`
}

func run() error {
	hermes := flag.String("hermes", "", "root hermes-agent (read-only)")
	parity := flag.String("parity", "../docs/FEATURE-PARITY.md", "FEATURE-PARITY.md")
	catalogPath := flag.String("catalog", "speed/catalog.json", "katalog op")
	out := flag.String("out", "speed/results/hermes.json", "artefak JSON")
	home := flag.String("home", "/tmp/hm_f0_speed", "HERMES_HOME (dibuat ulang)")
	only := flag.String("only", "", "ID dipisah koma: ukur ulang hanya ID ini (+ op baru); hasil lain diambil dari -out yang ada")
	flag.Parse()
	old, oldStartup, err := loadOld(*out, *only != "")
	if err != nil {
		return err
	}
	rerun := map[string]bool{}
	for _, id := range strings.Split(*only, ",") {
		rerun[strings.TrimSpace(id)] = true
	}
	reuse := func(id string) *speed.Result {
		if rerun[id] {
			return nil
		}
		return old[id]
	}
	if !strings.HasPrefix(*home, "/tmp/") || *hermes == "" {
		return fmt.Errorf("butuh -hermes dan -home di /tmp")
	}
	ptext, err := os.ReadFile(*parity)
	if err != nil {
		return err
	}
	craw, err := os.ReadFile(*catalogPath)
	if err != nil {
		return err
	}
	cat, err := speed.ParseCatalog(craw)
	if err != nil {
		return err
	}
	entries := speed.Classify(speed.ParseParity(string(ptext)), cat)

	if err := os.RemoveAll(*home); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(*home, "home"), 0o700); err != nil {
		return err
	}
	driver, err := filepath.Abs("speed/hermes_driver.py")
	if err != nil {
		return err
	}
	r := &speed.Runner{
		HermesDir: *hermes, Driver: driver, WS: filepath.Join(*home, "ws"), CLIRuns: cat.CLIRuns,
		Env: []string{
			"PATH=" + os.Getenv("PATH"), "LANG=C.UTF-8", "TERM=xterm-256color",
			"HOME=" + filepath.Join(*home, "home"), "HERMES_HOME=" + *home,
			"PYTHONDONTWRITEBYTECODE=1", "OPENROUTER_API_KEY=inspect-only",
		},
	}
	ctx := context.Background()
	if err := r.Prepare(ctx); err != nil {
		return err
	}

	// In-process dulu (satu proses driver), lalu PTY startup, lalu CLI cold start.
	var inproc []string
	for _, e := range entries {
		if e.Op != nil && e.Op.Kind != "cli" && reuse(e.ID) == nil {
			inproc = append(inproc, e.ID)
		}
	}
	ops := map[string]speed.Op{}
	for _, e := range entries {
		if e.Op != nil {
			ops[e.ID] = *e.Op
		}
	}
	res, err := r.RunInproc(ctx, inproc, ops, cat.InprocRuns)
	if err != nil {
		return err
	}
	startup := oldStartup
	if startup == nil || rerun["_startup_chat"] {
		if startup, err = r.RunPTY(ctx, cat.Ops["_startup_chat"]); err != nil {
			return err
		}
	}
	sum := summary{ByClass: map[string]int{}}
	for i := range entries {
		e := &entries[i]
		if e.Op != nil {
			switch prev := reuse(e.ID); {
			case prev != nil:
				e.Result = prev
			case e.Op.Kind == "cli":
				fmt.Fprintf(os.Stderr, "cli %s %v\n", e.ID, e.Op.Argv)
				e.Result = r.RunCLI(ctx, *e.Op)
			default:
				e.Result = res[e.ID]
			}
			if e.Result == nil || e.Result.Error != "" || len(e.Result.SamplesMS) == 0 {
				e.Class = speed.ClassNotMeasurable
				if e.Result != nil {
					e.Reason = "error offline (tanpa jaringan/kredensial): " + e.Result.Error
				} else {
					e.Reason = "driver tidak mengembalikan hasil"
				}
				sum.ErrorsToNM++
			} else if e.Op.Proxy {
				sum.MeasuredProxy++
			}
		}
		sum.ByClass[e.Class]++
	}
	sum.Rows = len(entries)
	if res["_cli_init"] != nil {
		fmt.Fprintf(os.Stderr, "HermesCLI init %.0f ms\n", res["_cli_init"].SamplesMS[0])
	}
	pyver, _ := exec.Command("python3", "--version").Output()
	rep := report{
		Meta: map[string]string{
			"hermes_dir": *hermes, "date": time.Now().Format(time.RFC3339), "goos": runtime.GOOS, "goarch": runtime.GOARCH,
			"python": strings.TrimSpace(string(pyver)), "network": "none (unshare -rn)",
			"cli_runs": fmt.Sprint(cat.CLIRuns), "inproc_runs": fmt.Sprint(cat.InprocRuns),
			"note": "cli = cold start proses penuh (1 warm-up); tool/slash/py = in-process warm; pty = startup chat interaktif",
		},
		Summary: sum, Startup: startup, Entries: entries,
	}
	slices.SortStableFunc(rep.Entries, func(a, b speed.Entry) int { return strings.Compare(a.ID, b.ID) })
	data, err := json.MarshalIndent(rep, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(*out, data, 0o644)
}

// loadOld membaca hasil sebelumnya (mode -only): ID → Result untuk baris yang punya op.
func loadOld(path string, enabled bool) (map[string]*speed.Result, *speed.Result, error) {
	if !enabled {
		return nil, nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var rep report
	if err := json.Unmarshal(data, &rep); err != nil {
		return nil, nil, err
	}
	m := map[string]*speed.Result{}
	for _, e := range rep.Entries {
		if e.Op != nil && e.Result != nil {
			m[e.ID] = e.Result
		}
	}
	return m, rep.Startup, nil
}
