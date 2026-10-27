// Command f3measure builds the F3 baseline artefact: Damn-Bot's TUI first-token
// latency through a real PTY and a timestamped fake OpenAI SSE provider, plus
// clean/incremental build time. The Hermes side is recorded as unavailable
// unless its pinned runtime is present; error informativeness is blocked on
// human raters. No number is invented.
package main

import (
	"archive/tar"
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

type firstTokenDim struct {
	Agent     string    `json:"agent"`
	Surface   string    `json:"surface"`
	Method    string    `json:"method"`
	Token     string    `json:"token"`
	Runs      int       `json:"runs"`
	SamplesMS []float64 `json:"samples_ms"`
	P50MS     float64   `json:"p50_ms"`
	P95MS     float64   `json:"p95_ms"`
}

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
		runs      = flag.Int("runs", 20, "first-token runs")
		buildRuns = flag.Int("build-runs", 3, "clean+incremental build pairs")
		skipFirst = flag.Bool("skip-first-token", false, "skip PTY measurement")
		skipBuild = flag.Bool("skip-buildtime", false, "skip build measurement")
		hermesSrc = flag.String("hermes-src", "", "optional Hermes checkout (diagnostic only)")
		hermesPy  = flag.String("hermes-python", "", "optional Hermes Python (diagnostic only)")
	)
	flag.Parse()
	if err := run(*bin, *out, *repo, *commit, *dirty, *runs, *buildRuns, *skipFirst, *skipBuild, *hermesSrc, *hermesPy); err != nil {
		fmt.Fprintln(os.Stderr, "f3measure:", err)
		os.Exit(1)
	}
}

func run(bin, out, repo, commit string, dirty, runs, buildRuns int, skipFirst, skipBuild bool, hermesSrc, hermesPy string) error {
	art := map[string]any{
		"meta": map[string]any{
			"phase":          "F3",
			"date":           time.Now().UTC().Format(time.RFC3339),
			"damnbot_commit": commit,
			"damnbot_dirty":  dirty,
			"cpu":            cpuModel(),
			"kernel":         uname(),
			"loadavg":        loadavg(),
			"go":             runtime.Version(),
			"harness":        "bench/f3/run.sh: cmd/f3measure (PTY + fake OpenAI SSE + go build timing)",
			"host_note":      "workstation measurement, not the Q14 reference machine",
		},
		"hermes":        "unavailable",
		"hermes_detail": hermesStatus(hermesSrc, hermesPy),
		"dimensions": map[string]any{
			"error_informatif": map[string]any{
				"status": "blocked",
				"reason": "requires 60 common-error cases and two human raters; no rubric or score was fabricated",
				"design": errorCaseDesign(),
			},
		},
	}
	if !skipFirst {
		if bin == "" {
			return errors.New("first-token measurement needs -bin")
		}
		dim, err := measureFirstToken(bin, runs)
		if err != nil {
			return fmt.Errorf("first token: %w", err)
		}
		art["dimensions"].(map[string]any)["first_token_tui"] = dim
	}
	if !skipBuild {
		dim, err := measureBuildtime(repo, buildRuns)
		if err != nil {
			return fmt.Errorf("buildtime: %w", err)
		}
		art["dimensions"].(map[string]any)["buildtime"] = dim
	}
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
	return nil
}

// ---- first token over a real PTY -------------------------------------------------

type fakeProvider struct {
	ln     net.Listener
	srv    *http.Server
	token  string
	mu     sync.Mutex
	first  time.Time
	closed sync.WaitGroup
}

func newFakeProvider() (*fakeProvider, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	f := &fakeProvider{ln: ln, token: fakeToken}
	f.srv = &http.Server{Handler: http.HandlerFunc(f.serve), ReadHeaderTimeout: 5 * time.Second}
	f.closed.Add(1)
	go func() {
		defer f.closed.Done()
		_ = f.srv.Serve(ln)
	}()
	return f, nil
}

func (f *fakeProvider) URL() string { return "http://" + f.ln.Addr().String() + "/v1" }

func (f *fakeProvider) First() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.first
}

func (f *fakeProvider) Close() {
	_ = f.srv.Close()
	f.closed.Wait()
}

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

func measureFirstToken(bin string, runs int) (firstTokenDim, error) {
	if _, err := exec.LookPath("script"); err != nil {
		return firstTokenDim{}, fmt.Errorf("PTY helper script(1): %w", err)
	}
	dim := firstTokenDim{
		Agent:   "damnbot",
		Surface: "TUI (real damnbot binary under a PTY)",
		Method:  "T0 = fake provider first content frame written; T1 = F3TOKEN visible in PTY output",
		Token:   fakeToken,
		Runs:    runs,
	}
	for i := 0; i < runs; i++ {
		ms, err := runTUIOnce(bin)
		if err != nil {
			return dim, fmt.Errorf("run %d/%d: %w", i+1, runs, err)
		}
		dim.SamplesMS = append(dim.SamplesMS, ms)
	}
	dim.P50MS, dim.P95MS = pct(dim.SamplesMS)
	return dim, nil
}

func runTUIOnce(bin string) (float64, error) {
	dir, err := os.MkdirTemp("", "damnbot-f3-tui-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(dir)
	home := filepath.Join(dir, "home")
	xdg := filepath.Join(dir, "xdg")
	data := filepath.Join(dir, "data")
	cfgDir := filepath.Join(xdg, "damnbot")
	for _, d := range []string{home, xdg, data, cfgDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return 0, err
		}
	}
	cfg := []byte("[provider]\nmodel = \"" + fakeModel + "\"\n")
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), cfg, 0o600); err != nil {
		return 0, err
	}
	fp, err := newFakeProvider()
	if err != nil {
		return 0, err
	}
	defer fp.Close()

	cmd := exec.Command("script", "-qefc", "stty rows 40 cols 120; exec "+shellQuote(bin), "/dev/null")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"HOME="+home, "XDG_CONFIG_HOME="+xdg, "XDG_DATA_HOME="+data,
		"TERM=xterm-256color", "COLUMNS=120", "LINES=40",
		"DAMNBOT_BENCH_MODEL="+fakeModel,
		"DAMNBOT_BENCH_BASE_URL="+fp.URL(),
		"DAMNBOT_BENCH_API_KEY=f3-bench",
	)
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

func readPTY(r io.Reader, out *lockedBuffer, promptCh chan<- struct{}, tokenCh chan<- time.Time) {
	promptSeen, tokenSeen := false, false
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		now := time.Now()
		if n > 0 {
			_, _ = out.Write(buf[:n])
			text := out.String()
			if !promptSeen && strings.Contains(text, "enter send") {
				promptSeen = true
				select {
				case promptCh <- struct{}{}:
				default:
				}
			}
			if !tokenSeen && strings.Contains(text, fakeToken) {
				tokenSeen = true
				select {
				case tokenCh <- now:
				default:
				}
			}
		}
		if err != nil {
			return
		}
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// ---- build time ------------------------------------------------------------------

func measureBuildtime(repo string, runs int) (buildDim, error) {
	var dim buildDim
	dim.Clean.Method = "go build -tags goolm ./cmd/damnbot with empty GOCACHE; source from git archive HEAD"
	dim.Incremental.Method = "same tree, append one comment to internal/loop/loop.go, rebuild with the clean build's GOCACHE"
	for i := 0; i < runs; i++ {
		src, err := os.MkdirTemp("", "damnbot-f3-build-")
		if err != nil {
			return dim, err
		}
		work := filepath.Join(src, "src")
		cache := filepath.Join(src, "gocache")
		for _, d := range []string{work, cache} {
			if err := os.MkdirAll(d, 0o700); err != nil {
				os.RemoveAll(src)
				return dim, err
			}
		}
		if err := gitArchive(repo, work); err != nil {
			os.RemoveAll(src)
			return dim, err
		}
		clean, err := timeBuild(work, cache, filepath.Join(src, "clean-bin"))
		if err != nil {
			os.RemoveAll(src)
			return dim, fmt.Errorf("clean build %d: %w", i+1, err)
		}
		dim.Clean.SamplesS = append(dim.Clean.SamplesS, clean)
		probe := filepath.Join(work, "internal", "loop", "loop.go")
		f, err := os.OpenFile(probe, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			os.RemoveAll(src)
			return dim, err
		}
		_, werr := f.WriteString("\n// f3 incremental build probe\n")
		cerr := f.Close()
		if werr != nil || cerr != nil {
			os.RemoveAll(src)
			return dim, errors.Join(werr, cerr)
		}
		inc, err := timeBuild(work, cache, filepath.Join(src, "inc-bin"))
		if err != nil {
			os.RemoveAll(src)
			return dim, fmt.Errorf("incremental build %d: %w", i+1, err)
		}
		dim.Incremental.SamplesS = append(dim.Incremental.SamplesS, inc)
		os.RemoveAll(src)
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
	cmd := exec.Command("git", "-C", repo, "archive", "--format=tar", "HEAD")
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	tr := tar.NewReader(out)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(hdr.Name)
		if name == "." || strings.HasPrefix(name, ".."+string(filepath.Separator)) || filepath.IsAbs(name) {
			return fmt.Errorf("archive path escapes: %q", hdr.Name)
		}
		target := filepath.Join(dst, name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode)&0o777)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		}
	}
	return cmd.Wait()
}

// ---- artefact helpers ------------------------------------------------------------

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

func hermesStatus(src, py string) map[string]any {
	st := map[string]any{
		"status": "unavailable",
		"reason": "pinned Hermes runtime is not provisioned (Python 3.14 + 45 pinned deps and Node workspace deps); no package install was attempted",
		"repro": []string{
			"HERMES_DIR=<hermes-868b9f244f checkout> HERMES_PYTHONPATH=<deps> bench/f2/run.sh",
			"python3.14 -m hermes_cli.main chat  # requires the pinned dependency set",
		},
		"f0_reference": []string{
			"bench/speed/results/hermes-868b9f244f-nollm.json",
			"bench/speed/results/hermes-868b9f244f-llm.json",
		},
	}
	if src != "" {
		st["source"] = "present (commit not verifiable: checkout has no .git)"
	}
	if py != "" {
		st["python"] = py
	}
	return st
}

func errorCaseDesign() []map[string]any {
	groups := []struct {
		name string
		eg   string
	}{
		{"config parse/unknown/type/range/invariant", "bad TOML, unknown key, max_turns=500"},
		{"credentials missing/invalid", "missing provider key, keychain denied"},
		{"provider transport", "401, 429, 5xx, timeout, network down"},
		{"database", "open/migrate/schema/permission/locked"},
		{"filesystem/workspace", "permission, symlink escape, disk full"},
		{"exec/approval", "shell denied, timeout, spawn failure"},
		{"tool schema/args", "bad args, unknown tool, invalid JSON"},
		{"context budget", "overflow, compaction failure"},
		{"budget/pricing", "cap reached, stale price"},
		{"terminal/TUI", "no TTY, unsupported TERM, resize"},
		{"import/input", "malformed Hermes/file input"},
		{"ACP/stdio", "bad frame, disconnect, oversized frame"},
	}
	out := make([]map[string]any, 0, len(groups))
	for _, g := range groups {
		out = append(out, map[string]any{"group": g.name, "cases": 5, "example": g.eg})
	}
	return out
}

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
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "model name") {
			_, v, ok := strings.Cut(line, ":")
			if ok {
				return strings.TrimSpace(v)
			}
		}
	}
	return ""
}

func uname() string {
	out, err := exec.Command("uname", "-sr").Output()
	if err != nil {
		return runtime.GOOS + "/" + runtime.GOARCH
	}
	return strings.TrimSpace(string(out))
}
