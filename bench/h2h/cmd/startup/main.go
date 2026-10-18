// Command startup mengukur TS-11: startup interaktif `hermes chat` sampai prompt siap, idle RSS,
// dan latensi respons pertama, dengan jaringan hermes hanya ke proxy rekam LLM.
//
// hermes berjalan di `unshare -rn` (hanya lo). Driver Python me-relay 127.0.0.1:PORT di netns
// ke unix socket proxy rekam di host, sehingga satu-satunya egress adalah upstream LLM.
//
//	cd bench && go run ./h2h/cmd/startup -hermes ~/Dokumen/hermes-agent -key-file /tmp/f0-secrets/llm_key \
//	    -upstream https://api.teamorouter.com -model gpt-5.4-mini -runs 5 \
//	    -out speed/results/hermes-868b9f244f-llm.json -rec h2h/results/2026-10-18/ts11.proxy.jsonl.gz
package main

import (
	"compress/gzip"
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
	"slices"
	"strings"
	"time"

	"github.com/damnbot/damnbot/bench/h2h"
)

const relayPort = 18080 // di dalam netns terisolasi; tidak bentrok dengan host

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type sample struct {
	FirstOutputMS   *float64 `json:"first_output_ms"`
	PromptReadyMS   *float64 `json:"prompt_ready_ms"`
	IdleRSSKB       int      `json:"idle_rss_kb"`
	FirstResponseMS *float64 `json:"first_response_ms"`
	DaemonsKilled   int      `json:"daemons_killed"`
}

type stat struct {
	N   int     `json:"n"`
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
}

// pct memakai nearest-rank; nilai nil (tidak teramati) dikeluarkan dan n mencerminkannya.
func pct(xs []float64) stat {
	if len(xs) == 0 {
		return stat{}
	}
	s := slices.Sorted(slices.Values(xs))
	rank := func(p float64) float64 {
		i := int(float64(len(s))*p+0.999999) - 1
		return s[max(0, min(i, len(s)-1))]
	}
	return stat{N: len(s), P50: rank(0.50), P95: rank(0.95)}
}

func networkDesc(netns bool) string {
	if netns {
		return "unshare -rn (hanya lo); egress hanya relay 127.0.0.1:18080 → unix socket proxy rekam → upstream LLM"
	}
	return "jaringan normal; provider hermes = proxy rekam TCP 127.0.0.1 (hanya trafik LLM yang direkam; fetch lain hermes, mis. model catalog, tidak lewat proxy)"
}

func run() error {
	hermesDir := flag.String("hermes", "", "checkout hermes-agent (read-only)")
	upstream := flag.String("upstream", "", "base URL provider tanpa /v1")
	keyFile := flag.String("key-file", "", "file kunci API upstream")
	model := flag.String("model", "", "id model")
	runs := flag.Int("runs", 5, "run terukur (ditambah 1 warm-up)")
	capUSD := flag.Float64("cap-usd", 0.05, "batas keras USD")
	priceIn := flag.Float64("price-in", 0.09, "USD/1M input")
	priceCached := flag.Float64("price-cached", 0.009, "USD/1M input ter-cache")
	priceOut := flag.Float64("price-out", 0.51, "USD/1M output")
	out := flag.String("out", "", "artefak JSON")
	recPath := flag.String("rec", "", "rekaman proxy (.jsonl.gz)")
	home := flag.String("hermes-home", "/tmp/hm_ts11", "HERMES_HOME")
	work := flag.String("work", "/tmp/h2h_ts11_work", "workdir + transkrip PTY per run")
	netns := flag.Bool("netns", false, "jalankan hermes di unshare -rn dengan egress hanya ke proxy")
	readyRE := flag.String("ready-re", "", "regex penanda prompt input di output PTY (ANSI dibuang)")
	flag.Parse()
	if *hermesDir == "" || *upstream == "" || *keyFile == "" || *model == "" || *out == "" || *recPath == "" {
		return errors.New("startup: -hermes, -upstream, -key-file, -model, -out, -rec wajib")
	}
	keyb, err := os.ReadFile(*keyFile)
	if err != nil {
		return err
	}
	up, err := url.Parse(*upstream)
	if err != nil {
		return err
	}
	f, err := os.Create(*recPath)
	if err != nil {
		return err
	}
	zw := gzip.NewWriter(f)
	price := h2h.Price{Input: *priceIn, CachedInput: *priceCached, Output: *priceOut}
	rec := &h2h.Recorder{Upstream: up, Key: strings.TrimSpace(string(keyb)), Price: price, CapUSD: *capUSD,
		Client: &http.Client{Timeout: 5 * time.Minute}}
	// -netns: hermes di `unshare -rn`, egress hanya lewat relay unix socket ke proxy.
	// Tanpa -netns: jaringan normal, provider hermes = proxy TCP lokal ini.
	var ln net.Listener
	sock, baseURL := "-", ""
	if *netns {
		sock = filepath.Join(os.TempDir(), fmt.Sprintf("h2h-ts11-%d.sock", os.Getpid()))
		if ln, err = net.Listen("unix", sock); err != nil {
			return err
		}
		defer os.Remove(sock)
		baseURL = fmt.Sprintf("http://127.0.0.1:%d/v1", relayPort)
	} else {
		if ln, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			return err
		}
		baseURL = "http://" + ln.Addr().String() + "/v1"
	}
	srv := &http.Server{Handler: rec, ReadHeaderTimeout: 30 * time.Second}
	go func() { _ = srv.Serve(ln) }()

	if err := os.RemoveAll(*home); err != nil {
		return err
	}
	if err := os.MkdirAll(*home, 0o700); err != nil {
		return err
	}
	cfg := fmt.Sprintf("model:\n  provider: custom\n  default: %q\n  base_url: %q\n  api_key: %q\n  api_mode: chat_completions\n",
		*model, baseURL, h2h.DummyKey)
	if err := os.WriteFile(filepath.Join(*home, "config.yaml"), []byte(cfg), 0o600); err != nil {
		return err
	}
	// Transkrip PTY per run (<work>/pty.json.runN.pty) disimpan sebagai bukti, tidak di-commit.
	if err := os.RemoveAll(*work); err != nil {
		return err
	}
	if err := os.MkdirAll(*work, 0o755); err != nil {
		return err
	}
	raw := filepath.Join(*work, "pty.json")

	driver, _ := filepath.Abs("speed/hermes_driver.py")
	argv := []string{"python3", driver, "ptyllm", raw, fmt.Sprint(*runs), sock, fmt.Sprint(relayPort),
		"--", "python3", "-m", "hermes_cli.main", "chat"}
	if *netns {
		argv = append([]string{"unshare", "-rn", "sh", "-c", `ip link set lo up && exec "$0" "$@"`}, argv...)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = *work
	cmd.Env = append(h2h.CleanEnv(), "HERMES_HOME="+*home, "HOME="+*home, "PYTHONPATH="+*hermesDir,
		"PYTHONDONTWRITEBYTECODE=1", "TERM=xterm-256color", "COLUMNS=120", "LINES=40", "H2H_READY_RE="+*readyRE)
	rec.Begin("hermes", "TS-11", zw)
	t0 := time.Now()
	outb, runErr := cmd.CombinedOutput()
	usage, perrs := rec.End()
	_ = srv.Close()
	h2h.StopHomeProcesses(*home)
	if err := zw.Close(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if runErr != nil {
		return fmt.Errorf("startup: driver: %w: %s", runErr, outb)
	}
	b, err := os.ReadFile(raw)
	if err != nil {
		return err
	}
	var res struct {
		Argv    []string `json:"argv"`
		Query   string   `json:"query"`
		Expect  string   `json:"expect"`
		ReadyRE string   `json:"ready_re"`
		Warmup  sample   `json:"warmup"`
		Runs    []sample `json:"runs"`
	}
	if err := json.Unmarshal(b, &res); err != nil {
		return err
	}
	var fo, pr, fr, rss []float64
	for _, s := range res.Runs {
		for _, p := range []struct {
			v   *float64
			dst *[]float64
		}{{s.FirstOutputMS, &fo}, {s.PromptReadyMS, &pr}, {s.FirstResponseMS, &fr}} {
			if p.v != nil {
				*p.dst = append(*p.dst, *p.v)
			}
		}
		rss = append(rss, float64(s.IdleRSSKB)/1024)
	}
	artifact := map[string]any{
		"meta": map[string]any{
			"date": time.Now().UTC().Format(time.RFC3339), "model": *model, "upstream": up.String(),
			"price_usd": price, "cap_usd": *capUSD, "wall_s": time.Since(t0).Seconds(),
			"network":        networkDesc(*netns),
			"prompt_ready":   "output PTY (ANSI dibuang) pertama kali cocok ready_re = " + res.ReadyRE + "; consent telemetry di-seed 'No thanks' sebelum run (setup)",
			"idle_rss":       "Σ VmRSS pohon proses (termasuk gateway daemon anak) setelah 10 s diam di prompt",
			"first_response": "query diketik per karakter, Enter, berhenti saat '" + res.Expect + "' muncul di output PTY; durasi request upstream ada di rekaman",
			"argv":           res.Argv, "query": res.Query, "recording": *recPath,
		},
		"summary": map[string]any{
			"first_output_ms": pct(fo), "prompt_ready_ms": pct(pr), "idle_rss_mb": pct(rss), "first_response_ms": pct(fr),
		},
		"usage": usage, "spent_usd": rec.Spent(), "harness_errors": append([]string{}, perrs...),
		"warmup": res.Warmup, "runs": res.Runs,
	}
	ob, err := json.MarshalIndent(artifact, "", " ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(*out, append(ob, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "prompt_ready %+v first_response %+v idle_rss_mb %+v calls %d spent %.4f harness_errors %d\n",
		pct(pr), pct(fr), pct(rss), usage.Calls, rec.Spent(), len(perrs))
	return nil
}
