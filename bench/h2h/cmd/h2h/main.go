// Command h2h menjalankan smoke LLM (ROADMAP F0 #5, F1 ADR-062) lewat proxy rekam bench/h2h:
// 10 task subset E1–E7 × 1 run, hermes vs Damn-Bot (N/A sampai F1).
//
//	cd bench && go run ./h2h/cmd/h2h -hermes ~/Dokumen/hermes-agent -key-file /tmp/f0-secrets/llm_key \
//	    -upstream https://api.teamorouter.com -model gpt-5.4-mini -out h2h/results/2026-10-18 \
//	    -price-in 0.09 -price-cached 0.009 -price-out 0.51
//
// Kunci upstream hanya dibaca dari -key-file dan hanya ada di memori proxy; agent memakai
// kunci dummy. Rekaman menyimpan header Authorization sebagai REDACTED.
//
// Biaya (ADR-062 butir 3, ADR-075 AUD-037, ADR-085): setiap biaya dicatat ke ledger tunggal
// h2h.F1LedgerPath (h2h/results/f1-spend.json, di-commit, cap total US$2,00), yang juga dipakai
// cmd/startup; cap berlaku untuk jumlah semua run. -cap-usd hanya bisa menurunkan cap ledger.
// Harga input/output wajib > 0.
package main

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/damnbot/damnbot/bench/evals"
	"github.com/damnbot/damnbot/bench/h2h"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type summary struct {
	Meta struct {
		Date         string    `json:"date"`
		Upstream     string    `json:"upstream"`
		Model        string    `json:"model"`
		Price        h2h.Price `json:"price_usd"`
		PriceSource  string    `json:"price_source"`
		CapUSD       float64   `json:"cap_usd"`
		Ledger       string    `json:"ledger"`
		HermesCommit string    `json:"hermes_commit"`
		EvalSet      string    `json:"evalset"`
		Selection    string    `json:"selection"`
		TimeoutS     float64   `json:"timeout_s_per_task"`
		MaxTurns     int       `json:"max_turns"`
		GOOS         string    `json:"goos"`
		GOARCH       string    `json:"goarch"`
	} `json:"meta"`
	Categories map[string]int `json:"categories"`
	Totals     struct {
		Tasks         int       `json:"tasks"`
		Ran           int       `json:"ran"`
		Passed        int       `json:"passed"`
		HarnessErrors int       `json:"harness_errors"`
		Usage         h2h.Usage `json:"usage"`
		SpentUSD      float64   `json:"spent_usd_proxy_total"` // run ini, termasuk request di luar task
		LedgerUSD     float64   `json:"ledger_spent_usd"`      // total ledger lintas run setelah run ini
		// Request di luar jendela task (background.proxy.jsonl.gz); error-nya ikut harness_errors.
		Background       h2h.Usage `json:"background_usage"`
		BackgroundErrors []string  `json:"background_errors"`
	} `json:"totals"`
	Damnbot string           `json:"damnbot"`
	Results []h2h.TaskResult `json:"results"`
}

// F1CapUSD: lihat h2h.F1CapUSD.
const F1CapUSD = h2h.F1CapUSD

func run() error {
	hermesDir := flag.String("hermes", "", "checkout hermes-agent (read-only; empty = Damn-Bot only)")
	damnbotBin := flag.String("damnbot", "", "binary cmd/damnbot (empty = hermes only)")
	evalDir := flag.String("evals", "evals/v1", "direktori eval set v1")
	out := flag.String("out", "", "direktori hasil (rekaman + summary.json)")
	upstream := flag.String("upstream", "", "base URL provider tanpa /v1, mis. https://api.example.com")
	keyFile := flag.String("key-file", "", "file berisi kunci API upstream (chmod 600)")
	model := flag.String("model", "", "id model")
	priceIn := flag.Float64("price-in", 0, "USD per 1M token input")
	priceCached := flag.Float64("price-cached", 0, "USD per 1M token input ter-cache")
	priceOut := flag.Float64("price-out", 0, "USD per 1M token output")
	priceSrc := flag.String("price-source", "", "sumber tabel harga")
	ledgerPath := new(string)
	*ledgerPath = h2h.F1LedgerPath
	capUSD := flag.Float64("cap-usd", 0, "batas keras USD; 0 = cap ledger (US$2,00 saat ledger dibuat, ADR-062); hanya bisa menurunkan")
	timeout := flag.Duration("timeout", 300*time.Second, "timeout per task (default 300 s)")
	maxTurns := flag.Int("max-turns", 20, "batas iterasi tool per task (hermes --max-turns)")
	taskRetries := flag.Int("task-retries", 2, "retry task pada upstream_unavailable")
	home := flag.String("hermes-home", "/tmp/hm_h2h", "HERMES_HOME (di-reset per task)")
	work := flag.String("work", "/tmp/h2h_work", "root workdir task")
	only := flag.String("only", "", "subset ID dipisah koma (debug)")
	smoke := flag.String("smoke", "f1", "f1 = smoke F1 campuran E1–E6; f5 = 10 task E1 (ROADMAP F5)")
	flag.Parse()
	if *keyFile == "" {
		home, _ := os.UserHomeDir()
		*keyFile = h2h.KeyFile(os.Getenv, home)
	}
	if *smoke == "f5" && *model == "" {
		*model = h2h.F5CheapModel
	}
	if *out == "" || *upstream == "" || *keyFile == "" || *model == "" || (*hermesDir == "" && *damnbotBin == "") {
		return errors.New("h2h: -out, -upstream, -key-file (or H2H_KEY_FILE/~/.config/damnbot/llm_key), -model and one of -hermes/-damnbot are required")
	}
	if *smoke != "f1" && *smoke != "f5" {
		return errors.New("h2h: -smoke must be f1 or f5")
	}
	price := h2h.Price{Input: *priceIn, CachedInput: *priceCached, Output: *priceOut}
	if !price.Valid() {
		return errors.New("h2h: -price-in dan -price-out wajib > 0, -price-cached ≥ 0 (ADR-075/085: harga 0 mematikan cap)")
	}
	phase, phaseCap := h2h.PhaseF1, h2h.F1CapUSD
	if *smoke == "f5" {
		phase, phaseCap = h2h.PhaseF5, h2h.F5CapUSD
	}
	ledger, err := h2h.OpenLedgerPhase(*ledgerPath, phase, phaseCap)
	if err != nil {
		return err
	}
	keyb, err := os.ReadFile(*keyFile)
	if err != nil {
		return fmt.Errorf("h2h: key: %w", err)
	}
	up, err := url.Parse(*upstream)
	if err != nil {
		return fmt.Errorf("h2h: upstream: %w", err)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}

	var tasks []evals.Task
	if *smoke == "f5" {
		tasks, err = h2h.SelectF5E1(*evalDir, exec.LookPath)
	} else {
		tasks, err = h2h.Select(*evalDir, exec.LookPath)
	}
	if err != nil {
		return err
	}
	if *only != "" {
		keep := map[string]bool{}
		for _, id := range strings.Split(*only, ",") {
			keep[id] = true
		}
		var sub = tasks[:0]
		for _, t := range tasks {
			if keep[t.ID] {
				sub = append(sub, t)
			}
		}
		tasks = sub
	}

	if ledger.Spent() >= ledger.CapUSD {
		return fmt.Errorf("h2h: ledger %s habis: %.4f / %.2f USD", *ledgerPath, ledger.Spent(), ledger.CapUSD)
	}
	spentBefore := ledger.Spent()
	// Refuse before spending anything when the worst-case projection for this
	// run (FirstGuess per task per attempt) would exceed the remaining cap.
	const firstGuess = 0.02
	projected := spentBefore + float64(len(tasks))*firstGuess*float64(1+*taskRetries)
	if projected > ledger.CapUSD {
		return fmt.Errorf("h2h: projected worst case %.4f USD + spent %.4f > cap %.2f; refusing to start", projected, spentBefore, ledger.CapUSD)
	}
	bgFile, err := os.Create(filepath.Join(*out, "background.proxy.jsonl.gz"))
	if err != nil {
		return err
	}
	bgZip := gzip.NewWriter(bgFile)
	rec := &h2h.Recorder{Upstream: up, Key: strings.TrimSpace(string(keyb)), Price: price, CapUSD: *capUSD, Ledger: ledger,
		Client: &http.Client{Timeout: 10 * time.Minute}, Background: bgZip}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	psrv := &http.Server{Handler: rec, ReadHeaderTimeout: 30 * time.Second}
	go func() { _ = psrv.Serve(ln) }()
	defer psrv.Close()

	r := &h2h.Runner{
		Recorder: rec, ProxyURL: "http://" + ln.Addr().String() + "/v1",
		OutDir: *out, WorkRoot: *work, Timeout: *timeout, TaskRetries: *taskRetries, FirstGuess: firstGuess, Log: os.Stderr,
	}
	if *damnbotBin != "" {
		r.Damnbot = &h2h.Damnbot{Binary: *damnbotBin, Model: *model, ProviderAttempts: 2}
	} else {
		r.Hermes = &h2h.Hermes{Dir: *hermesDir, Home: *home, Python: "python3", Model: *model, MaxTurns: *maxTurns}
	}
	results := r.Run(context.Background(), tasks)
	_ = psrv.Close()
	if err := bgZip.Close(); err != nil {
		return err
	}
	if err := bgFile.Close(); err != nil {
		return err
	}

	var s summary
	s.Meta.Date = time.Now().UTC().Format(time.RFC3339)
	s.Meta.Upstream, s.Meta.Model, s.Meta.Price, s.Meta.PriceSource, s.Meta.CapUSD = up.String(), *model, price, *priceSrc, rec.Cap()
	s.Meta.Ledger = *ledgerPath
	s.Meta.EvalSet = "evalset/v1 " + *evalDir
	s.Meta.Selection = fmt.Sprintf("per set %v: task eligible dengan sha256(%q+id) terkecil", h2h.SmokeQuota, h2h.SelectSalt)
	s.Meta.TimeoutS, s.Meta.MaxTurns, s.Meta.GOOS, s.Meta.GOARCH = timeout.Seconds(), *maxTurns, runtime.GOOS, runtime.GOARCH
	if *hermesDir != "" {
		if b, err := exec.Command("git", "-C", *hermesDir, "rev-parse", "HEAD").Output(); err == nil {
			s.Meta.HermesCommit = strings.TrimSpace(string(b))
		}
	}
	s.Damnbot = h2h.DamnbotNA
	if *damnbotBin != "" {
		s.Damnbot = "damnbot run (F1 integration) via " + *damnbotBin
	}
	s.Categories = map[string]int{}
	for _, res := range results {
		s.Categories[res.Category]++
	}
	s.Results = results
	s.Totals.Tasks = len(results)
	for _, res := range results {
		if res.Status == "ran" {
			s.Totals.Ran++
		}
		if res.Verdict != nil && res.Verdict.Pass {
			s.Totals.Passed++
		}
		s.Totals.HarnessErrors += len(res.HarnessErrors)
		s.Totals.Usage.Add(res.Usage)
	}
	s.Totals.SpentUSD = ledger.Spent() - spentBefore
	s.Totals.LedgerUSD = ledger.Spent()
	s.Totals.Background, s.Totals.BackgroundErrors = rec.BackgroundStats()
	if s.Totals.BackgroundErrors == nil {
		s.Totals.BackgroundErrors = []string{}
	}
	s.Totals.HarnessErrors += len(s.Totals.BackgroundErrors)
	b, err := json.MarshalIndent(s, "", " ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*out, "summary.json"), append(b, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "ran %d/%d pass %d harness_errors %d calls %d spent %.4f USD\n",
		s.Totals.Ran, s.Totals.Tasks, s.Totals.Passed, s.Totals.HarnessErrors, s.Totals.Usage.Calls, s.Totals.SpentUSD)
	return nil
}
