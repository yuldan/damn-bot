// Command f3errors captures the F3 informative-error suite and its gate report.
package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Case struct {
	ID                  string            `json:"id"`
	Group               string            `json:"group"`
	Argv                []string          `json:"argv"`
	Env                 map[string]string `json:"env"`
	Setup               []Setup           `json:"setup"`
	ExpectExitNonzero   bool              `json:"expect_exit_nonzero"`
	WhatUserShouldLearn string            `json:"what_user_should_learn"`
}

type Setup struct {
	Op      string `json:"op"`
	Path    string `json:"path"`
	Target  string `json:"target"`
	Content string `json:"content"`
	Mode    string `json:"mode"`
	Size    int64  `json:"size"`
	Offset  int64  `json:"offset"`
	Hex     string `json:"hex"`
}

type Capture struct {
	ID         string   `json:"id"`
	Group      string   `json:"group"`
	Argv       []string `json:"argv"`
	ExitCode   int      `json:"exit_code"`
	DurationMS int64    `json:"duration_ms"`
	Stdout     string   `json:"stdout"`
	Stderr     string   `json:"stderr"`
}

type Summary struct {
	Schema      string   `json:"schema"`
	Cases       int      `json:"cases"`
	Groups      int      `json:"groups"`
	ExitNonzero int      `json:"exit_nonzero"`
	SecretLeaks int      `json:"secret_leaks"`
	Panic       int      `json:"panic_or_goroutine"`
	NonEmpty    int      `json:"non_empty"`
	Failed      []string `json:"failed"`
}

var (
	sessionRe = regexp.MustCompile(`"session_id":"[0-9a-f]{32}"`)
	secretRe  = []*regexp.Regexp{
		regexp.MustCompile(`sk-[A-Za-z0-9_-]{20,}`),
		regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/-]{8,}`),
		regexp.MustCompile(`(?i)api[_-]?key\s*[=:]\s*["']?[A-Za-z0-9._~+/-]{4,}`),
		regexp.MustCompile(`ghp_[A-Za-z0-9]{36}`),
		regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
	}
	cleanChecksum = regexp.MustCompile(`[0-9a-f]{64}`)
)

func main() { os.Exit(run()) }

func run() int {
	bin := flag.String("bin", "", "path to the damnbot binary")
	casesPath := flag.String("cases", "f3/errors/cases.json", "cases.json path")
	out := flag.String("out", "f3/errors", "output directory")
	timeout := flag.Duration("timeout", 120*time.Second, "per-case timeout")
	flag.Parse()
	if *bin == "" {
		return fail(errors.New("--bin is required"))
	}
	tmp, err := os.MkdirTemp("", "damnbot-f3-errors-")
	if err != nil {
		return fail(err)
	}
	defer cleanTree(tmp)
	for _, d := range []string{"home", "tmp"} {
		if err := os.MkdirAll(filepath.Join(tmp, d), 0o700); err != nil {
			return fail(err)
		}
	}
	data, err := os.ReadFile(*casesPath)
	if err != nil {
		return fail(err)
	}
	var suite struct{ Cases []Case }
	if err := json.Unmarshal(data, &suite); err != nil {
		return fail(err)
	}
	if err := validateCases(suite.Cases); err != nil {
		return fail(err)
	}
	srv, err := startFake()
	if err != nil {
		return fail(err)
	}
	defer srv.close()
	if err := makeTemplate(*bin, tmp, srv.base); err != nil {
		return fail(err)
	}
	capturedDir := filepath.Join(*out, "captured")
	if err := os.RemoveAll(capturedDir); err != nil {
		return fail(err)
	}
	if err := os.MkdirAll(capturedDir, 0o700); err != nil {
		return fail(err)
	}
	hostname, _ := os.Hostname()
	san := newSanitizer(tmp, *bin, srv.base, srv.port, os.Getenv("USER"), hostname, nil)
	sum := Summary{Schema: "damnbot.f3.errors.summary/v1", Cases: len(suite.Cases), Groups: 12, Failed: []string{}}
	for _, c := range suite.Cases {
		cap, keys := runCase(*bin, tmp, srv.base, srv.port, c, *timeout)
		san.secrets = keys
		cap.Argv = san.argv(cap.Argv)
		cap.Stdout = san.clean(cap.Stdout)
		cap.Stderr = san.clean(cap.Stderr)
		if err := writeJSON(filepath.Join(capturedDir, c.ID+".json"), cap); err != nil {
			return fail(err)
		}
		body := cap.Stdout + "\n" + cap.Stderr
		if cap.ExitCode == 0 {
			sum.Failed = append(sum.Failed, c.ID+": exit 0")
		} else {
			sum.ExitNonzero++
		}
		for _, re := range secretRe {
			if re.MatchString(body) {
				sum.SecretLeaks++
				sum.Failed = append(sum.Failed, c.ID+": secret pattern")
			}
		}
		if strings.Contains(body, "panic:") || strings.Contains(body, "goroutine ") {
			sum.Panic++
			sum.Failed = append(sum.Failed, c.ID+": panic/goroutine")
		}
		if strings.TrimSpace(body) == "" {
			sum.Failed = append(sum.Failed, c.ID+": empty output")
		} else {
			sum.NonEmpty++
		}
	}
	finding := Case{ID: "finding-text-output-empty", Group: "finding", Argv: []string{"run", "-p", "hi", "--model", "openai/fake", "--data-dir", "{tmp}/data", "--base-url", "http://127.0.0.1:1/v1", "--api-key", "f3-dummy-key", "--provider-attempts", "1", "--output", "text"}}
	fcap, keys := runCase(*bin, tmp, srv.base, srv.port, finding, *timeout)
	san.secrets = keys
	fcap.Argv, fcap.Stdout, fcap.Stderr = san.argv(fcap.Argv), san.clean(fcap.Stdout), san.clean(fcap.Stderr)
	if err := writeJSON(filepath.Join(capturedDir, finding.ID+".json"), fcap); err != nil {
		return fail(err)
	}
	if err := writeJSON(filepath.Join(*out, "summary.json"), sum); err != nil {
		return fail(err)
	}
	fmt.Printf("cases: %d in %d groups\n", sum.Cases, sum.Groups)
	fmt.Printf("exit_nonzero: %d/%d\n", sum.ExitNonzero, sum.Cases)
	fmt.Printf("secret_leaks: %d/%d\n", sum.SecretLeaks, sum.Cases)
	fmt.Printf("panic_or_goroutine: %d/%d\n", sum.Panic, sum.Cases)
	fmt.Printf("non_empty: %d/%d\n", sum.NonEmpty, sum.Cases)
	if len(sum.Failed) > 0 {
		fmt.Println("FAILED:")
		for _, f := range sum.Failed {
			fmt.Println(" -", f)
		}
		return 1
	}
	fmt.Println("gate: PASS")
	return 0
}

func validateCases(cs []Case) error {
	groups := map[string]int{}
	ids := map[string]bool{}
	for _, c := range cs {
		if c.ID == "" || c.Group == "" || c.WhatUserShouldLearn == "" {
			return fmt.Errorf("case with empty id/group/learning: %+v", c)
		}
		if ids[c.ID] {
			return fmt.Errorf("duplicate id %s", c.ID)
		}
		if !c.ExpectExitNonzero {
			return fmt.Errorf("%s: expect_exit_nonzero must be true", c.ID)
		}
		ids[c.ID] = true
		groups[c.Group]++
	}
	if len(cs) != 60 || len(groups) != 12 {
		return fmt.Errorf("need 60 cases in 12 groups, got %d in %d", len(cs), len(groups))
	}
	for g, n := range groups {
		if n != 5 {
			return fmt.Errorf("group %s has %d cases, want 5", g, n)
		}
	}
	return nil
}

func runCase(bin, tmp, base string, port int, c Case, timeout time.Duration) (Capture, []string) {
	dir := filepath.Join(tmp, "cases", c.ID)
	_ = os.MkdirAll(dir, 0o700)
	var secrets []string
	if err := setupCase(dir, tmp, c.Setup); err != nil {
		return Capture{ID: c.ID, Group: c.Group, ExitCode: -1, Stderr: "setup: " + err.Error()}, nil
	}
	argv := make([]string, len(c.Argv))
	for i, a := range c.Argv {
		argv[i] = expand(a, dir, base)
	}
	for i, a := range argv {
		if a == "--api-key" && i+1 < len(argv) {
			secrets = append(secrets, argv[i+1])
		}
	}
	env := baseEnv(tmp)
	for k, v := range c.Env {
		env = setEnv(env, k, expand(v, dir, base))
		if strings.Contains(strings.ToUpper(k), "KEY") || strings.Contains(strings.ToUpper(k), "TOKEN") {
			secrets = append(secrets, v)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, argv...)
	cmd.Dir = dir
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	start := time.Now()
	err := cmd.Run()
	dur := time.Since(start).Milliseconds()
	code := 0
	if err != nil {
		code = -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
		if ctx.Err() != nil {
			fmt.Fprintf(&stderr, "\n[runner] timeout after %s\n", timeout)
		}
	}
	return Capture{ID: c.ID, Group: c.Group, Argv: argv, ExitCode: code, DurationMS: dur, Stdout: stdout.String(), Stderr: stderr.String()}, secrets
}

func setupCase(dir, tmp string, ops []Setup) error {
	for _, s := range ops {
		p := expand(s.Path, dir, "")
		content := expand(s.Content, dir, "")
		if err := applySetup(s, dir, tmp, p, content); err != nil {
			return fmt.Errorf("%s %s: %w", s.Op, p, err)
		}
	}
	return nil
}

func applySetup(s Setup, dir, tmp, p, content string) error {
	switch s.Op {
	case "mkdir":
		return os.MkdirAll(p, 0o755)
	case "write":
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		return os.WriteFile(p, []byte(content), 0o644)
	case "fill":
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		b := []byte(strings.Repeat(content, int(s.Size/int64(len(content))+1)))
		return os.WriteFile(p, b[:s.Size], 0o644)
	case "chmod":
		m, err := strconv.ParseUint(s.Mode, 8, 32)
		if err != nil {
			return err
		}
		return os.Chmod(p, fs.FileMode(m))
	case "symlink":
		return os.Symlink(s.Target, p)
	case "copy_db":
		return copyDir(filepath.Join(tmp, "template"), p)
	case "truncate":
		return os.Truncate(p, s.Size)
	case "patch":
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		h, err := hex.DecodeString(s.Hex)
		if err != nil {
			return err
		}
		if s.Offset < 0 || int(s.Offset)+len(h) > len(b) {
			return fmt.Errorf("patch outside file")
		}
		copy(b[s.Offset:], h)
		return os.WriteFile(p, b, 0o644)
	case "corrupt_checksum":
		return corruptChecksum(filepath.Join(p, "damnbot.db"))
	case "write_holder":
		hdir := filepath.Join(p, "run", "maint.holders")
		if err := os.MkdirAll(hdir, 0o700); err != nil {
			return err
		}
		holder := map[string]any{"owner_host": "f3-other-host", "owner_pid": 999999, "owner_start": "0", "boot_id": "f3", "kind": "tui", "schema_max": 1}
		b, _ := json.Marshal(holder)
		return os.WriteFile(filepath.Join(hdir, "999999"), b, 0o600)
	default:
		return fmt.Errorf("unknown op")
	}
}

func corruptChecksum(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	loc := cleanChecksum.FindIndex(b)
	if loc == nil {
		return fmt.Errorf("no 64-hex checksum found")
	}
	if b[loc[0]] == '0' {
		b[loc[0]] = '1'
	} else {
		b[loc[0]] = '0'
	}
	return os.WriteFile(path, b, 0o644)
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0o700)
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			info, _ := d.Info()
			return os.MkdirAll(target, info.Mode().Perm())
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, info.Mode().Perm())
	})
}

func makeTemplate(bin, tmp, base string) error {
	dir := filepath.Join(tmp, "template")
	env := baseEnv(tmp)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "run", "-p", "seed", "--model", "openai/fake", "--data-dir", dir,
		"--base-url", base+"/ok/v1", "--api-key", "f3-dummy-key", "--provider-attempts", "1", "--output", "json")
	cmd.Env = env
	cmd.Dir = tmp
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("seed run: %v: %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "damnbot.db")); err != nil {
		return fmt.Errorf("seed db: %w", err)
	}
	return nil
}

func baseEnv(tmp string) []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + filepath.Join(tmp, "home"),
		"TMPDIR=" + filepath.Join(tmp, "tmp"),
		"LANG=C", "LC_ALL=C", "TZ=UTC", "NO_COLOR=1",
	}
}

func setEnv(env []string, key, val string) []string {
	out := env[:0]
	for _, e := range env {
		if !strings.HasPrefix(e, key+"=") {
			out = append(out, e)
		}
	}
	return append(out, key+"="+val)
}

func expand(s, dir, base string) string {
	s = strings.ReplaceAll(s, "{tmp}", dir)
	s = strings.ReplaceAll(s, "{server}", base)
	s = strings.ReplaceAll(s, "{port}", base[strings.LastIndex(base, ":")+1:])
	return s
}

type sanitizer struct {
	pairs   [][2]string
	secrets []string
	port    string
	user    string
}

func newSanitizer(tmp, bin, base string, port int, user, host string, secrets []string) *sanitizer {
	home, _ := os.UserHomeDir()
	pairs := [][2]string{{tmp, "{TMP}"}, {home, "{HOME}"}, {bin, "{BIN}"}, {base, "{SERVER}"}}
	if host != "" {
		pairs = append(pairs, [2]string{host, "{HOST}"})
	}
	s := &sanitizer{pairs: pairs, secrets: secrets, port: ":" + strconv.Itoa(port)}
	s.user = user
	return s
}

func (s *sanitizer) clean(in string) string {
	for _, p := range s.pairs {
		if p[0] != "" {
			in = strings.ReplaceAll(in, p[0], p[1])
		}
	}
	for _, v := range s.secrets {
		if len(v) >= 4 {
			in = strings.ReplaceAll(in, v, "{REDACTED}")
		}
	}
	if s.user != "" {
		in = regexp.MustCompile(`\b`+regexp.QuoteMeta(s.user)+`\b`).ReplaceAllString(in, "{USER}")
	}
	in = sessionRe.ReplaceAllString(in, `"session_id":"{SESSION_ID}"`)
	in = strings.ReplaceAll(in, s.port, ":{PORT}")
	return in
}

func (s *sanitizer) argv(in []string) []string {
	out := make([]string, len(in))
	for i, a := range in {
		out[i] = s.clean(a)
		if (a == "--api-key" || a == "-api-key") && i+1 < len(out) {
			out[i+1] = "{REDACTED}"
		}
	}
	return out
}

func fail(err error) int {
	fmt.Fprintln(os.Stderr, "f3errors:", err)
	return 2
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

func cleanTree(root string) {
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			_ = os.Chmod(p, 0o700)
		} else {
			_ = os.Chmod(p, 0o600)
		}
		return nil
	})
	_ = os.RemoveAll(root)
}

// fake provider -------------------------------------------------------------

type fakeServer struct {
	ln   net.Listener
	srv  *http.Server
	base string
	port int
}

func startFake() (*fakeServer, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	f := &fakeServer{ln: ln, base: "http://" + ln.Addr().String(), port: port}
	f.srv = &http.Server{Handler: http.HandlerFunc(f.handle), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = f.srv.Serve(ln) }()
	return f, nil
}

func (f *fakeServer) close() { _ = f.srv.Close() }

func (f *fakeServer) handle(w http.ResponseWriter, r *http.Request) {
	mode := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")[0]
	_, _ = io.Copy(io.Discard, r.Body)
	switch mode {
	case "auth-missing":
		if r.Header.Get("Authorization") != "" {
			writeSSE(w, 400, "application/json", nil, `{"error":{"message":"unexpected auth","type":"invalid_request_error"}}`)
			return
		}
		writeSSE(w, 401, "application/json", nil, `{"error":{"message":"No API key provided","type":"invalid_request_error","code":"missing_api_key"}}`)
	case "auth-invalid":
		writeSSE(w, 401, "application/json", nil, `{"error":{"message":"Incorrect API key provided","type":"invalid_request_error","code":"invalid_api_key"}}`)
	case "forbidden":
		writeSSE(w, 403, "application/json", nil, `{"error":{"message":"account disabled","type":"permission_error","code":"account_deactivated"}}`)
	case "model-missing":
		writeSSE(w, 404, "application/json", nil, `{"error":{"message":"model does not exist","type":"invalid_request_error","code":"model_not_found"}}`)
	case "model-invalid":
		writeSSE(w, 400, "application/json", nil, `{"error":{"message":"invalid model name","type":"invalid_request_error","code":"invalid_model"}}`)
	case "rate-limit":
		writeSSE(w, 429, "application/json", map[string]string{"Retry-After": "1"}, `{"error":{"message":"rate limit reached","type":"rate_limit_error"}}`)
	case "server-error":
		writeSSE(w, 500, "application/json", nil, `{"error":{"message":"internal server error","type":"server_error"}}`)
	case "unavailable":
		writeSSE(w, 503, "application/json", nil, `{"error":{"message":"service unavailable","type":"server_error"}}`)
	case "redirect":
		writeSSE(w, 302, "text/plain", map[string]string{"Location": "/ok/v1/chat/completions"}, "")
	case "malformed-sse":
		writeSSE(w, 200, "text/event-stream", nil, "data: {not json}\n\n")
	case "html-body":
		writeSSE(w, 200, "text/html", nil, "<html>oops</html>")
	case "empty-sse":
		writeSSE(w, 200, "text/event-stream", nil, "data: [DONE]\n\n")
	case "no-choices":
		writeSSE(w, 200, "text/event-stream", nil, sseChunk(map[string]any{"choices": []any{}}))
	case "slow-header":
		select {
		case <-time.After(35 * time.Second):
		case <-r.Context().Done():
			return
		}
		writeSSE(w, 200, "text/event-stream", nil, sseChunk(finalChunk("late")))
	case "tool-shell", "tool-unknown", "tool-badargs", "tool-traversal", "tool-required":
		writeSSE(w, 200, "text/event-stream", nil, sseChunk(toolChunk(mode)))
	default:
		writeSSE(w, 200, "text/event-stream", nil, sseChunk(finalChunk("hello")))
	}
}

func writeSSE(w http.ResponseWriter, status int, ctype string, hdr map[string]string, body string) {
	w.Header().Set("Content-Type", ctype)
	for k, v := range hdr {
		w.Header().Set(k, v)
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func sseChunk(v map[string]any) string {
	b, _ := json.Marshal(v)
	return "data: " + string(b) + "\n\ndata: [DONE]\n\n"
}

func finalChunk(text string) map[string]any {
	return map[string]any{"id": "x", "object": "chat.completion.chunk", "model": "m",
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": text}, "finish_reason": "stop"}}}
}

func toolChunk(mode string) map[string]any {
	name, args := "read_file", `{"path":"x"}`
	switch mode {
	case "tool-shell":
		name, args = "shell", `{"command":"echo hi"}`
	case "tool-unknown":
		name, args = "made_up_tool", `{}`
	case "tool-badargs":
		name, args = "read_file", `{"path":`
	case "tool-traversal":
		name, args = "read_file", `{"path":"../../../../etc/shadow"}`
	case "tool-required":
		name, args = "write_file", `{"path":"/tmp/escape"}`
	}
	tc := map[string]any{"index": 0, "id": "call_1", "type": "function", "function": map[string]any{"name": name, "arguments": args}}
	return map[string]any{"id": "x", "object": "chat.completion.chunk", "model": "m",
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{tc}}, "finish_reason": "tool_calls"}}}
}
