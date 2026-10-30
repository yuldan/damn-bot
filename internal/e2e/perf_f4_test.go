//go:build linux

package e2e

// The F4 budget harness (ROADMAP F4, SCORECARD §1.2): it runs the real
// `damnbot serve` binary with the fake Telegram Bot API, the fake provider and
// (through the `servetest` hook, because production serve does not wire cron
// yet) the real cron scheduler with a one-minute script job. After a warmup it
// samples VmRSS, VmHWM, idle CPU from /proc/<pid>/stat and the goroutine gauge
// from /metrics, then enforces RSS ≤ 90 MB and idle CPU ≤ 0.5 %.
//
// `make perf-f4` runs both TestPerfF4 and TestPerfF4NegativeControl; the
// negative control proves the gate notices an injected 128 MB + spinning
// goroutines load. Both write an artefact under bench/f4/results/.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/netx"
)

const (
	f4RSSLimitMB  = 90.0
	f4CPULimitPct = 0.5
	// f4ClockTicks is Linux USER_HZ; getconf CLK_TCK is 100 on every x86
	// kernel this gate targets (the harness records it in the artefact).
	f4ClockTicks = 100.0
)

// f4Result is the artefact of one measurement.
type f4Result struct {
	Meta        f4Meta        `json:"meta"`
	Measurement f4Measurement `json:"measurement"`
	Cron        f4Cron        `json:"cron"`
	Gate        f4Gate        `json:"gate"`
	Negative    bool          `json:"negative_control"`
}

type f4Meta struct {
	Phase         string  `json:"phase"`
	Date          string  `json:"date"`
	DamnbotCommit string  `json:"damnbot_commit"`
	DamnbotDirty  string  `json:"damnbot_dirty"`
	Binary        string  `json:"binary"`
	GoVersion     string  `json:"go_version"`
	GOMAXPROCS    string  `json:"gomaxprocs"`
	CPU           string  `json:"cpu"`
	Kernel        string  `json:"kernel"`
	LoadavgStart  string  `json:"loadavg_start"`
	LoadavgEnd    string  `json:"loadavg_end"`
	WarmupS       int     `json:"warmup_s"`
	WindowS       int     `json:"window_s"`
	ClockTicks    float64 `json:"clock_ticks"`
	HostNote      string  `json:"host_note"`
}

type f4Measurement struct {
	RSSKB         int     `json:"rss_kb"`
	PeakKB        int     `json:"peak_kb"`
	RSSMB         float64 `json:"rss_mb"`
	PeakMB        float64 `json:"peak_mb"`
	IdleCPUPct    float64 `json:"idle_cpu_percent"`
	CPUSeconds    float64 `json:"cpu_seconds"`
	CPUTicksDelta uint64  `json:"cpu_ticks_delta"`
	Goroutines    int     `json:"goroutines"`
	TelegramPolls int     `json:"telegram_polls"`
	PID           int     `json:"pid"`
}

type f4Cron struct {
	Mode   string `json:"mode"`
	Runs   int    `json:"runs"`
	Reason string `json:"reason,omitempty"`
}

type f4Gate struct {
	RSSLimitMB   float64  `json:"rss_limit_mb"`
	CPULimitPct  float64  `json:"cpu_limit_percent"`
	Pass         bool     `json:"pass"`
	FailedReason []string `json:"failed_reasons,omitempty"`
}

// TestPerfF4 measures the idle footprint of the F4 daemon and enforces the
// budget. It is skipped unless make perf-f4 sets DAMNBOT_PERF_F4=1.
func TestPerfF4(t *testing.T) {
	if os.Getenv("DAMNBOT_PERF_F4") != "1" {
		t.Skip("perf gate: run `make perf-f4`")
	}
	sc := f4Scenario{name: "damnbot", warmup: f4Duration("DAMNBOT_F4_WARMUP", 10*time.Second), window: f4Duration("DAMNBOT_F4_WINDOW", 60*time.Second), cron: true}
	res := measureF4(t, sc)
	writeF4Artifact(t, "damnbot", &res)
	logF4(t, sc.name, &res)
	if !res.Gate.Pass {
		t.Fatalf("F4 budget exceeded: %v", res.Gate.FailedReason)
	}
}

// TestPerfF4NegativeControl injects 128 MB of touched heap and four spinning
// goroutines; the same gate must reject the result for both reasons.
func TestPerfF4NegativeControl(t *testing.T) {
	if os.Getenv("DAMNBOT_PERF_F4") != "1" {
		t.Skip("perf gate: run `make perf-f4`")
	}
	sc := f4Scenario{
		name: "negative-control", warmup: 3 * time.Second, window: 5 * time.Second, cron: true,
		env: []string{"DAMNBOT_SERVE_TEST_LOAD_MB=128", "DAMNBOT_SERVE_TEST_BURN=4"},
	}
	res := measureF4(t, sc)
	res.Negative = true
	writeF4Artifact(t, "damnbot-negative-control", &res)
	logF4(t, sc.name, &res)
	if res.Gate.Pass {
		t.Fatalf("negative control passed the gate: rss=%.1f MB cpu=%.3f%%", res.Measurement.RSSMB, res.Measurement.IdleCPUPct)
	}
	for _, want := range []string{"rss", "cpu"} {
		if !strings.Contains(strings.Join(res.Gate.FailedReason, " "), want) {
			t.Fatalf("negative control did not trip %s: %v", want, res.Gate.FailedReason)
		}
	}
}

type f4Scenario struct {
	name           string
	warmup, window time.Duration
	cron           bool
	env            []string
}

// measureF4 starts one serve child and samples it after the warmup.
func measureF4(t *testing.T, sc f4Scenario) f4Result {
	t.Helper()
	e := New(t)
	tg := NewFakeTG(t, FakeTGOptions{})
	env := append(gatewayServeEnv(e, tg), "GOMAXPROCS=2")
	cronOn := sc.cron && os.Getenv("DAMNBOT_F4_CRON") != "0"
	if cronOn {
		env = append(env, "DAMNBOT_SERVE_TEST_CRON=1")
	}
	env = append(env, sc.env...)
	start := loadavg()
	p := startServe(t, e, binPaths.serve, env)
	pid := p.cmd.Process.Pid
	<-time.After(sc.warmup)
	first, err := sampleProc(pid)
	if err != nil {
		t.Fatalf("sample %s: %v", sc.name, err)
	}
	<-time.After(sc.window)
	second, err := sampleProc(pid)
	if err != nil {
		t.Fatalf("sample %s: %v", sc.name, err)
	}
	goroutines := scrapeGoroutines(t, p.addr)
	polls := len(tg.RequestsFor("getUpdates"))
	cronRuns := strings.Count(p.stderr.String(), "cron run ok job="+perfCronJobIDConst)
	if cronOn && cronRuns == 0 {
		t.Fatalf("cron load did not execute a scheduled job\nstderr:\n%s", p.stderr.String())
	}
	gatewayStop(t, p)
	elapsed := second.at.Sub(first.at).Seconds()
	return f4Result{
		Meta: f4Meta{
			Phase: "F4", Date: time.Now().UTC().Format(time.RFC3339),
			DamnbotCommit: os.Getenv("DAMNBOT_F4_COMMIT"), DamnbotDirty: os.Getenv("DAMNBOT_F4_DIRTY"),
			Binary: "cmd/damnbot (tags goolm,servetest)", GoVersion: runtime.Version(),
			GOMAXPROCS: os.Getenv("GOMAXPROCS"), CPU: cpuModel(), Kernel: kernelRelease(),
			LoadavgStart: start, LoadavgEnd: loadavg(),
			WarmupS: int(sc.warmup.Seconds()), WindowS: int(sc.window.Seconds()), ClockTicks: f4ClockTicks,
			HostNote: "workstation measurement, not the Q14 reference machine",
		},
		Measurement: f4Measurement{
			RSSKB: second.rssKB, PeakKB: second.peakKB,
			RSSMB: mb(second.rssKB), PeakMB: mb(second.peakKB),
			IdleCPUPct: idleCPU(first, second, elapsed), CPUSeconds: cpuSeconds(first, second),
			CPUTicksDelta: second.ticks - first.ticks,
			Goroutines:    goroutines, TelegramPolls: polls, PID: pid,
		},
		Cron: f4Cron{Mode: cronMode(cronOn), Runs: cronRuns, Reason: cronReason(cronOn)},
		Gate: f4Gate{
			RSSLimitMB: f4RSSLimitMB, CPULimitPct: f4CPULimitPct, Pass: true,
		},
	}
}

// cronReason states plainly that production serve does not start the
// scheduler yet, so the measured cron load comes from the servetest seam.
func cronReason(on bool) string {
	if !on {
		return "not started"
	}
	return "serve does not wire internal/cron on this revision; the servetest hook runs the real scheduler with a one-minute script job"
}

// perfCronJobIDConst mirrors cmd/damnbot's perfCronJobID (a test cannot
// import package main). Both sides change together.
const perfCronJobIDConst = "perf-f4-cron"

// procSample is one /proc/<pid> reading.
type procSample struct {
	rssKB, peakKB int
	ticks         uint64 // process-wide utime+stime in USER_HZ
	at            time.Time
}

func sampleProc(pid int) (procSample, error) {
	status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return procSample{}, fmt.Errorf("read status: %w", err)
	}
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return procSample{}, fmt.Errorf("read stat: %w", err)
	}
	s := procSample{at: time.Now()}
	for line := range strings.SplitSeq(string(status), "\n") {
		switch {
		case strings.HasPrefix(line, "VmRSS:"):
			s.rssKB = firstInt(line)
		case strings.HasPrefix(line, "VmHWM:"):
			s.peakKB = firstInt(line)
		}
	}
	ticks, err := procTicks(string(stat))
	if err != nil {
		return procSample{}, err
	}
	s.ticks = ticks
	if s.rssKB == 0 {
		return procSample{}, errors.New("VmRSS missing")
	}
	return s, nil
}

// procTicks returns utime+stime from /proc/<pid>/stat, splitting after the
// final ')' because comm may contain spaces and parentheses.
func procTicks(stat string) (uint64, error) {
	i := strings.LastIndexByte(stat, ')')
	if i < 0 {
		return 0, errors.New("stat: no comm")
	}
	f := strings.Fields(stat[i+1:])
	if len(f) < 13 {
		return 0, fmt.Errorf("stat: %d fields after comm", len(f))
	}
	utime, err := strconv.ParseUint(f[11], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("utime: %w", err)
	}
	stime, err := strconv.ParseUint(f[12], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("stime: %w", err)
	}
	return utime + stime, nil
}

func idleCPU(first, second procSample, secs float64) float64 {
	if secs <= 0 {
		return 0
	}
	return cpuSeconds(first, second) / secs * 100
}

// cpuSeconds is the process-wide delta of utime+stime (all OS threads), the
// same counters pidstat reads (SCORECARD §1.2). Tick granularity is 10 ms;
// the 60 s window keeps the resolution at 0.017 %.
func cpuSeconds(first, second procSample) float64 {
	return float64(second.ticks-first.ticks) / f4ClockTicks
}

// scrapeGoroutines reads the damnbot_goroutines gauge from /metrics.
func scrapeGoroutines(t *testing.T, addr string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr+"/metrics", nil)
	if err != nil {
		t.Fatalf("metrics request: %v", err)
	}
	client := netx.New(netx.Config{}).Client("perf-f4", netx.Tool)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("metrics: %v", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("metrics close: %v", cerr)
		}
	}()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("metrics read: %v", err)
	}
	for line := range strings.SplitSeq(string(body), "\n") {
		rest, ok := strings.CutPrefix(line, "damnbot_goroutines ")
		if !ok {
			continue
		}
		n, cerr := strconv.Atoi(strings.TrimSpace(rest))
		if cerr != nil {
			t.Fatalf("goroutine gauge %q: %v", line, cerr)
		}
		return n
	}
	t.Fatal("damnbot_goroutines missing from /metrics")
	return 0
}

func firstInt(line string) int {
	for f := range strings.FieldsSeq(line) {
		if n, err := strconv.Atoi(f); err == nil {
			return n
		}
	}
	return 0
}

func mb(kb int) float64 { return float64(kb) / 1024 }

func loadavg() string {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func cpuModel() string {
	b, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return ""
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "model name"); ok {
			return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(v), ":"))
		}
	}
	return ""
}

// f4Duration reads a diagnostic override for the warmup/window.
func f4Duration(name string, def time.Duration) time.Duration {
	d, err := time.ParseDuration(os.Getenv(name))
	if err != nil || d <= 0 {
		return def
	}
	return d
}

// cronMode names the load source in the artefact.
func cronMode(on bool) string {
	if !on {
		return "off"
	}
	return "servetest-hook"
}

func kernelRelease() string {
	b, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// writeF4Artifact writes bench/f4/results/<name>-<sha>-F4.json.
func writeF4Artifact(t *testing.T, name string, res *f4Result) {
	t.Helper()
	res.Gate = evaluateF4Gate(res.Measurement.RSSMB, res.Measurement.IdleCPUPct)
	root, err := moduleRoot()
	if err != nil {
		t.Fatalf("artifact root: %v", err)
	}
	sha := res.Meta.DamnbotCommit
	if sha == "" {
		sha = "unknown"
	}
	dir := filepath.Join(root, "bench", "f4", "results")
	if xerr := os.MkdirAll(dir, 0o750); xerr != nil {
		t.Fatalf("artifact dir: %v", xerr)
	}
	b, err := json.MarshalIndent(res, "", " ")
	if err != nil {
		t.Fatalf("artifact marshal: %v", err)
	}
	b = append(b, '\n')
	out := filepath.Join(dir, fmt.Sprintf("%s-%s-F4.json", name, sha))
	if err := os.WriteFile(out, b, 0o600); err != nil {
		t.Fatalf("artifact write: %v", err)
	}
	t.Logf("artifact: %s", out)
}

func evaluateF4Gate(rssMB, cpuPct float64) f4Gate {
	g := f4Gate{RSSLimitMB: f4RSSLimitMB, CPULimitPct: f4CPULimitPct, Pass: true}
	if rssMB > f4RSSLimitMB {
		g.Pass = false
		g.FailedReason = append(g.FailedReason, fmt.Sprintf("rss %.1f MB > %.0f MB", rssMB, f4RSSLimitMB))
	}
	if cpuPct > f4CPULimitPct {
		g.Pass = false
		g.FailedReason = append(g.FailedReason, fmt.Sprintf("cpu %.3f%% > %.1f%%", cpuPct, f4CPULimitPct))
	}
	return g
}

func logF4(t *testing.T, name string, res *f4Result) {
	t.Helper()
	t.Logf("F4 %s: pid=%d rss=%.1f MB peak=%.1f MB cpu_idle=%.3f%% goroutines=%d polls=%d cron_runs=%d gate=%v %v",
		name, res.Measurement.PID, res.Measurement.RSSMB, res.Measurement.PeakMB, res.Measurement.IdleCPUPct,
		res.Measurement.Goroutines, res.Measurement.TelegramPolls, res.Cron.Runs, res.Gate.Pass, res.Gate.FailedReason)
}
