// Command f3measure writes the F3 baseline artefact over a real PTY.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	fakeModel  = "openai/gpt-fake"
	fakeToken  = "F3TOKEN"
	fakePrompt = "f3-prompt"
)

type series struct {
	Method   string    `json:"method"`
	SamplesS []float64 `json:"samples_s"`
	P50S     float64   `json:"p50_s"`
	P95S     float64   `json:"p95_s"`
}

type buildDim struct {
	Clean       series `json:"clean"`
	Incremental series `json:"incremental"`
}

func main() {
	var (
		bin       = flag.String("bin", "", "path to damnbot built with -tags goolm,bench")
		out       = flag.String("out", "", "artefact JSON path")
		repo      = flag.String("repo", "..", "repository root")
		commit    = flag.String("commit", "", "measured commit")
		dirty     = flag.Int("dirty", 0, "uncommitted paths at measurement")
		runs      = flag.Int("runs", 20, "PTY runs per scenario")
		buildRuns = flag.Int("build-runs", 3, "clean+incremental build pairs")
	)
	flag.Parse()
	if err := run(*bin, *out, *repo, *commit, *dirty, *runs, *buildRuns); err != nil {
		fmt.Fprintln(os.Stderr, "f3measure:", err)
		os.Exit(1)
	}
}

func run(bin, out, repo, commit string, dirty, runs, buildRuns int) error {
	art := map[string]any{
		"meta": map[string]any{
			"phase":          "F3",
			"date":           time.Now().UTC().Format(time.RFC3339),
			"damnbot_commit": commit,
			"damnbot_dirty":  dirty,
			"cpu":            cpuModel(),
			"kernel":         runtime.GOOS + "/" + runtime.GOARCH,
			"loadavg":        loadavg(),
			"go":             runtime.Version(),
			"harness":        "bench/f3/run.sh: cmd/f3measure (PTY + fake OpenAI SSE + go build timing)",
			"host_note":      "workstation measurement, not the Q14 reference machine",
		},
		"hermes":        "unavailable",
		"hermes_detail": "unavailable: pinned Hermes runtime not provisioned; no package install attempted; see bench/f3/README.md",
		"dimensions": map[string]any{
			"error_informatif": "blocked: human raters; 12 groups x 5 cases in README; no rubric or score fabricated",
		},
	}
	if bin == "" {
		return errors.New("PTY measurements need -bin")
	}
	startup, startupErr := measureStartup(bin, runs)
	art["dimensions"].(map[string]any)["startup_p12"] = startup
	dim, err := measureFirstToken(bin, runs)
	if err != nil {
		return fmt.Errorf("first token: %w", err)
	}
	art["dimensions"].(map[string]any)["first_token_tui"] = dim
	build, err := measureBuildtime(repo, buildRuns)
	if err != nil {
		return fmt.Errorf("buildtime: %w", err)
	}
	art["dimensions"].(map[string]any)["buildtime"] = build
	if out == "" {
		return errors.New("-out is required")
	}
	b, err := json.MarshalIndent(art, "", " ")
	if err != nil {
		return fmt.Errorf("marshal artefact: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return fmt.Errorf("create artefact dir: %w", err)
	}
	if err := os.WriteFile(out, append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("write artefact: %w", err)
	}
	fmt.Fprintf(os.Stderr, "wrote %s\n", out)
	if startupErr != nil {
		return fmt.Errorf("startup: %w", startupErr)
	}
	return nil
}

type fakeProvider struct {
	ln    net.Listener
	srv   *http.Server
	token string
	mu    sync.Mutex
	first time.Time
}

func newFakeProvider() (*fakeProvider, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	f := &fakeProvider{ln: ln, token: fakeToken}
	f.srv = &http.Server{Handler: http.HandlerFunc(f.serve), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = f.srv.Serve(ln) }()
	return f, nil
}

func (f *fakeProvider) URL() string { return "http://" + f.ln.Addr().String() + "/v1" }

func (f *fakeProvider) First() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.first
}

func (f *fakeProvider) Close() { _ = f.srv.Close() }

func (f *fakeProvider) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	hasPrompt := strings.Contains(string(body), fakePrompt)
	stream, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no flusher", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	token := "aux"
	if hasPrompt {
		token = f.token
	}
	frames := []string{
		fmt.Sprintf(`{"choices":[{"index":0,"delta":{"content":%q}}]}`, token),
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
	}
	if hasPrompt {
		f.mu.Lock()
		if f.first.IsZero() {
			f.first = time.Now()
		}
		f.mu.Unlock()
	}
	for _, frame := range frames {
		if _, err := io.WriteString(w, "data: "+frame+"\n\n"); err != nil {
			return
		}
		stream.Flush()
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	stream.Flush()
}

func measureFirstToken(bin string, runs int) (map[string]any, error) {
	if _, err := exec.LookPath("script"); err != nil {
		return nil, fmt.Errorf("PTY helper script(1): %w", err)
	}
	dim := map[string]any{"agent": "damnbot", "surface": "TUI (real damnbot binary under a PTY)", "method": "T0 = fake provider first content frame written; T1 = F3TOKEN visible in PTY output", "token": fakeToken, "runs": runs}
	samples := make([]float64, 0, runs)
	for i := 0; i < runs; i++ {
		ms, err := runTUIOnce(bin)
		if err != nil {
			return dim, fmt.Errorf("run %d/%d: %w", i+1, runs, err)
		}
		samples = append(samples, ms)
	}
	p50, p95 := pct(samples)
	dim["samples_ms"], dim["p50_ms"], dim["p95_ms"] = samples, p50, p95
	return dim, nil
}

func tuiCommand(bin string, extra []string, trace bool) (*exec.Cmd, func(), error) {
	dir, err := os.MkdirTemp("", "damnbot-f3-tui-")
	if err != nil {
		return nil, nil, err
	}
	home, xdg, data := filepath.Join(dir, "home"), filepath.Join(dir, "xdg"), filepath.Join(dir, "data")
	cfgDir := filepath.Join(xdg, "damnbot")
	for _, d := range []string{home, xdg, data, cfgDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			os.RemoveAll(dir)
			return nil, nil, err
		}
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte("[provider]\nmodel = \""+fakeModel+"\"\n"), 0o600); err != nil {
		os.RemoveAll(dir)
		return nil, nil, err
	}
	cmd := exec.Command("script", "-qefc", "stty rows 40 cols 120; exec "+shellQuote(bin), "/dev/null")
	cmd.Dir = dir
	env := append(os.Environ(), "HOME="+home, "XDG_CONFIG_HOME="+xdg, "XDG_DATA_HOME="+data,
		"TERM=xterm-256color", "COLUMNS=120", "LINES=40")
	env = append(env, extra...)
	if trace {
		env = append(env, "DAMNBOT_BENCH_TRACE=1")
	}
	cmd.Env = env
	return cmd, func() { os.RemoveAll(dir) }, nil
}

func runTUIOnce(bin string) (float64, error) {
	fp, err := newFakeProvider()
	if err != nil {
		return 0, err
	}
	defer fp.Close()
	cmd, cleanup, err := tuiCommand(bin, []string{
		"DAMNBOT_BENCH_MODEL=" + fakeModel,
		"DAMNBOT_BENCH_BASE_URL=" + fp.URL(),
		"DAMNBOT_BENCH_API_KEY=f3-bench",
	}, false)
	if err != nil {
		return 0, err
	}
	defer cleanup()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return 0, err
	}
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = stdin.Close()
		_ = pw.Close()
		_ = pr.Close()
		_, _ = cmd.Process.Wait()
	}()

	promptCh := make(chan struct{}, 1)
	tokenCh := make(chan time.Time, 1)
	output := &lockedBuffer{}
	go readPTY(pr, output, promptCh, tokenCh)
	select {
	case <-promptCh:
	case <-time.After(8 * time.Second):
		return 0, fmt.Errorf("TUI prompt not ready: %.500q", output.String())
	}
	if _, err := io.WriteString(stdin, fakePrompt+"\r"); err != nil {
		return 0, err
	}
	var tokenAt time.Time
	select {
	case tokenAt = <-tokenCh:
	case <-time.After(15 * time.Second):
		return 0, fmt.Errorf("token not rendered: %.500q", output.String())
	}
	first := fp.First()
	if first.IsZero() {
		return 0, errors.New("fake provider never saw the prompt request")
	}
	return float64(tokenAt.Sub(first).Microseconds()) / 1000, nil
}

type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func watchPTY(r io.Reader, out *lockedBuffer, onText func(string, time.Time)) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		now := time.Now()
		if n > 0 {
			_, _ = out.Write(buf[:n])
			onText(out.String(), now)
		}
		if err != nil {
			return
		}
	}
}

func readPTY(r io.Reader, out *lockedBuffer, promptCh chan<- struct{}, tokenCh chan<- time.Time) {
	prompt, token := false, false
	watchPTY(r, out, func(text string, now time.Time) {
		if !prompt && strings.Contains(text, "enter send") {
			prompt = true
			select {
			case promptCh <- struct{}{}:
			default:
			}
		}
		if !token && strings.Contains(text, fakeToken) {
			token = true
			select {
			case tokenCh <- now:
			default:
			}
		}
	})
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

type startupNumbers struct {
	Runs      int       `json:"runs"`
	SamplesMS []float64 `json:"samples_ms"`
	P50MS     float64   `json:"p50_ms"`
	P95MS     float64   `json:"p95_ms"`
}

type startupDim struct {
	startupNumbers
	ThresholdMS float64        `json:"threshold_ms"`
	Gate        string         `json:"gate"`
	Lazy        map[string]any `json:"lazy_start"`
	GOMAXPROCS1 startupNumbers `json:"gomaxprocs1"`
}

func measureStartup(bin string, runs int) (startupDim, error) {
	base, err := startupRuns(bin, runs, nil)
	if err != nil {
		return startupDim{}, err
	}
	one, err := startupRuns(bin, runs, []string{"GOMAXPROCS=1"})
	if err != nil {
		return startupDim{}, err
	}
	lazy, err := lazyStart(bin)
	if err != nil {
		return startupDim{}, err
	}
	dim := startupDim{startupNumbers: base, ThresholdMS: 100, Gate: "pass", Lazy: lazy, GOMAXPROCS1: one}
	if base.P95MS > dim.ThresholdMS {
		dim.Gate = "fail"
		return dim, fmt.Errorf("P12 p95 %.3f ms > %.0f ms", base.P95MS, dim.ThresholdMS)
	}
	return dim, nil
}

func startupRuns(bin string, runs int, extra []string) (startupNumbers, error) {
	out := startupNumbers{Runs: runs}
	for i := 0; i < runs; i++ {
		ms, _, err := runStartupOnce(bin, extra, false)
		if err != nil {
			return out, fmt.Errorf("run %d/%d: %w", i+1, runs, err)
		}
		out.SamplesMS = append(out.SamplesMS, ms)
	}
	out.P50MS, out.P95MS = pct(out.SamplesMS)
	return out, nil
}
func runStartupOnce(bin string, extra []string, trace bool) (float64, string, error) {
	cmd, cleanup, err := tuiCommand(bin, extra, trace)
	if err != nil {
		return 0, "", err
	}
	defer cleanup()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return 0, "", err
	}
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	t0 := time.Now()
	if err := cmd.Start(); err != nil {
		return 0, "", err
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = stdin.Close()
		_ = pw.Close()
		_ = pr.Close()
		_, _ = cmd.Process.Wait()
	}()
	out := &lockedBuffer{}
	frameCh := make(chan time.Time, 1)
	go waitFrame(pr, out, frameCh)
	var at time.Time
	select {
	case at = <-frameCh:
	case <-time.After(8 * time.Second):
		return 0, out.String(), fmt.Errorf("first frame not seen: %.500q", out.String())
	}
	return float64(at.Sub(t0).Microseconds()) / 1000, out.String(), nil
}

func waitFrame(r io.Reader, out *lockedBuffer, ch chan<- time.Time) {
	watchPTY(r, out, func(text string, now time.Time) {
		if strings.Contains(text, "enter send") {
			select {
			case ch <- now:
			default:
			}
		}
	})
}

func lazyStart(bin string) (map[string]any, error) {
	_, out, err := runStartupOnce(bin, nil, true)
	if err != nil {
		return nil, err
	}
	frame := strings.Index(out, "enter send")
	at := func(phase string) int { return strings.Index(out, "F3TRACE "+phase) }
	lazy := map[string]any{"method": "F3TRACE lines on stderr; frame marker 'enter send'; absence of ensure-start before frame"}
	for _, phase := range []string{"config-done", "app-built", "run-start"} {
		i := at(phase)
		lazy[phase+"_before_frame"] = i >= 0 && i < frame
	}
	ensure := at("ensure-start")
	before := ensure >= 0 && ensure < frame
	lazy["trace_seen"] = at("config-done") >= 0 && at("app-built") >= 0 && at("run-start") >= 0
	lazy["ensure_started_before_frame"] = before
	lazy["db_or_provider_before_frame"] = before
	if !lazy["trace_seen"].(bool) {
		return lazy, errors.New("lazy-start trace missing; build with -tags goolm,bench")
	}
	if before {
		return lazy, errors.New("lazy start violated: ensure (ctxdb/provider) ran before the first frame")
	}
	return lazy, nil
}

func measureBuildtime(repo string, runs int) (buildDim, error) {
	var dim buildDim
	dim.Clean.Method = "go build -tags goolm ./cmd/damnbot with empty GOCACHE; source from git archive HEAD"
	dim.Incremental.Method = "same tree, append one comment to internal/loop/loop.go, rebuild with the clean build's GOCACHE"
	for i := 0; i < runs; i++ {
		src, err := os.MkdirTemp("", "damnbot-f3-build-")
		if err != nil {
			return dim, err
		}
		defer os.RemoveAll(src)
		work := filepath.Join(src, "src")
		cache := filepath.Join(src, "gocache")
		for _, d := range []string{work, cache} {
			if err := os.MkdirAll(d, 0o700); err != nil {
				os.RemoveAll(src)
				return dim, err
			}
		}
		if err := gitArchive(repo, work); err != nil {
			return dim, err
		}
		clean, err := timeBuild(work, cache, filepath.Join(src, "clean-bin"))
		if err != nil {
			return dim, fmt.Errorf("clean build %d: %w", i+1, err)
		}
		dim.Clean.SamplesS = append(dim.Clean.SamplesS, clean)
		probe := filepath.Join(work, "internal", "loop", "loop.go")
		f, err := os.OpenFile(probe, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return dim, err
		}
		_, werr := f.WriteString("\n// f3 incremental build probe\n")
		cerr := f.Close()
		if werr != nil || cerr != nil {
			return dim, errors.Join(werr, cerr)
		}
		inc, err := timeBuild(work, cache, filepath.Join(src, "inc-bin"))
		if err != nil {
			return dim, fmt.Errorf("incremental build %d: %w", i+1, err)
		}
		dim.Incremental.SamplesS = append(dim.Incremental.SamplesS, inc)
	}
	dim.Clean.P50S, dim.Clean.P95S = pct(dim.Clean.SamplesS)
	dim.Incremental.P50S, dim.Incremental.P95S = pct(dim.Incremental.SamplesS)
	return dim, nil
}

func timeBuild(dir, cache, out string) (float64, error) {
	cmd := exec.Command("go", "build", "-tags", "goolm", "-mod=readonly", "-o", out, "./cmd/damnbot")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOCACHE="+cache, "CGO_ENABLED=0", "GOTOOLCHAIN=local")
	start := time.Now()
	combined, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("go build: %w: %.500s", err, combined)
	}
	return time.Since(start).Seconds(), nil
}

func gitArchive(repo, dst string) error {
	cmd := exec.Command("sh", "-c", `git -C "$1" archive --format=tar HEAD | tar -x -C "$2"`, "sh", repo, dst)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git archive: %w: %.300s", err, out)
	}
	return nil
}

func pct(xs []float64) (float64, float64) {
	if len(xs) == 0 {
		return 0, 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	rank := func(p float64) float64 {
		i := int(float64(len(s))*p+0.999999) - 1
		if i < 0 {
			i = 0
		}
		if i >= len(s) {
			i = len(s) - 1
		}
		return s[i]
	}
	return rank(0.50), rank(0.95)
}

func loadavg() string {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func cpuModel() string {
	out, _ := exec.Command("sh", "-c", "sed -n 's/^model name[[:space:]]*: //p' /proc/cpuinfo | head -1").Output()
	return strings.TrimSpace(string(out))
}
