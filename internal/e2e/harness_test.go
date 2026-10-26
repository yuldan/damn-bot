// Package e2e is the F2 end-to-end acceptance suite. Every scenario runs the
// real damnbot binary as a child process against a fake OpenAI-compatible
// provider and temporary data/workspace directories; nothing is exercised
// in-process. Other F2 features add *_test.go files to this package.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

const modelRef = "openai/gpt-fake"

// binPath is built once per test run by TestMain.
var binPath string //nolint:gochecknoglobals // one binary for the package's child processes

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "damnbot-e2e-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e: temp dir:", err)
		os.Exit(1) //nolint:forbidigo // TestMain owns the test process exit
	}
	name := "damnbot"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	binPath = filepath.Join(dir, name)
	root, err := moduleRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
		os.Exit(1) //nolint:forbidigo // TestMain owns the test process exit
	}
	build := exec.CommandContext(context.Background(), "go", "build", "-tags", "goolm", "-o", binPath, "./cmd/damnbot") //nolint:gosec // fixed tool, fixture path
	build.Dir = root
	if out, berr := build.CombinedOutput(); berr != nil {
		fmt.Fprintf(os.Stderr, "e2e: build damnbot: %v\n%s\n", berr, out)
		os.Exit(1) //nolint:forbidigo // TestMain owns the test process exit
	}
	code := m.Run()
	if rerr := os.RemoveAll(dir); rerr != nil && code == 0 {
		fmt.Fprintln(os.Stderr, "e2e: remove temp bin:", rerr)
		code = 1
	}
	os.Exit(code) //nolint:forbidigo // TestMain owns the test process exit
}

// moduleRoot finds the repository root from this package's directory.
func moduleRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getwd: %w", err)
	}
	root := filepath.Clean(filepath.Join(wd, "..", ".."))
	if _, serr := os.Stat(filepath.Join(root, "go.mod")); serr != nil {
		return "", fmt.Errorf("module root %s: %w", root, serr)
	}
	return root, nil
}

// Env is one child-process scenario: a fake provider, a temp data dir, a temp
// workspace and a private config/home so the host machine is never touched.
type Env struct {
	t       *testing.T
	Server  *providertest.Server
	DataDir string
	Workdir string
	cfg     string
	home    string
}

// New starts the fake provider with the scripted replies and creates the
// temporary directories.
func New(t *testing.T, replies ...providertest.Reply) *Env {
	t.Helper()
	srv := providertest.New(t, nil, replies...)
	dir := t.TempDir()
	e := &Env{t: t, Server: srv, DataDir: filepath.Join(dir, "data"), Workdir: filepath.Join(dir, "work"), home: filepath.Join(dir, "home")}
	for _, d := range []string{e.DataDir, e.Workdir, e.home} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	e.cfg = filepath.Join(dir, "config.toml")
	if err := os.WriteFile(e.cfg, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return e
}

// NewTrusted is New for a scenario that exercises tool behaviour inside a
// trusted workspace: the child's trust store (<config>/trusted-projects, the
// file `damnbot skills trust add` writes) registers the harness workspace, so
// SPEC §14 applies the trusted variant (shell_allow/--unsafe-auto-shell can
// auto-run, workspace reads keep their own source label). New stays untrusted,
// and the scenario that asserts the untrusted refusals uses it.
func NewTrusted(t *testing.T, replies ...providertest.Reply) *Env {
	t.Helper()
	e := New(t, replies...)
	trustWorkspace(t, e)
	return e
}

// trustWorkspace registers the harness workspace in the child's trust store.
func trustWorkspace(t *testing.T, e *Env) {
	t.Helper()
	dir := spillConfigDir(e.home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "trusted-projects"), []byte(e.Workdir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Result is one completed child process.
type Result struct {
	Stdout, Stderr string
	Code           int
}

// Run executes one `run` turn with the standard fake-provider flags.
func (e *Env) Run(prompt string, extra ...string) Result {
	e.t.Helper()
	args := []string{
		"run", "-p", prompt,
		"--model", modelRef,
		"--base-url", e.Server.URL,
		"--api-key", "e2e",
		"--data-dir", e.DataDir,
		"--config", e.cfg,
		"--offline",
		"--output", "json",
	}
	return e.Exec(append(args, extra...)...)
}

// Exec runs the built binary with args in the temp workspace. Callers of
// future commands (`undo`, `review`, `sessions`, `backup`, …) use this.
func (e *Env) Exec(args ...string) Result {
	e.t.Helper()
	return e.ExecEnv(nil, args...)
}

// ExecEnv runs the built binary with extra environment entries appended (the
// child still inherits the harness HOME/XDG_CONFIG_HOME).
func (e *Env) ExecEnv(env []string, args ...string) Result {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(e.t.Context(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binPath, args...) //nolint:gosec // binary path is this package's temp build
	cmd.Dir = e.Workdir
	// HOME/XDG are what the Unix loaders read; Windows resolves its config and
	// data roots from these instead (internal/config/paths.go baseDirs), so a
	// child without them would read the host profile and never see the
	// harness's trust store, journals or sessions.
	cmd.Env = append(os.Environ(),
		"HOME="+e.home,
		"XDG_CONFIG_HOME="+e.home,
		"USERPROFILE="+e.home,
		"APPDATA="+filepath.Join(e.home, "AppData", "Roaming"),
		"LOCALAPPDATA="+filepath.Join(e.home, "AppData", "Local"),
	)
	cmd.Env = append(cmd.Env, env...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	res := Result{Stdout: out.String(), Stderr: errb.String()}
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			e.t.Fatalf("exec %v: %v", args, err)
		}
		res.Code = exit.ExitCode()
	}
	return res
}

// RequireOK fails unless the child exited 0.
func (r Result) RequireOK(t *testing.T) {
	t.Helper()
	if r.Code != 0 {
		t.Fatalf("exit = %d\nstdout:\n%s\nstderr:\n%s", r.Code, r.Stdout, r.Stderr)
	}
}

// Requests returns the provider request bodies in order.
func (e *Env) Requests() [][]byte { return e.Server.Requests() }

// RequestJSON decodes one provider request body.
func (e *Env) RequestJSON(t *testing.T, i int) map[string]any {
	t.Helper()
	raw := e.Requests()
	if i < 0 || i >= len(raw) {
		t.Fatalf("request %d missing (got %d)", i, len(raw))
	}
	var body map[string]any
	if err := json.Unmarshal(raw[i], &body); err != nil {
		t.Fatalf("request %d: %v", i, err)
	}
	return body
}

// RequestText is the raw wire body of request i.
func (e *Env) RequestText(t *testing.T, i int) string {
	t.Helper()
	raw := e.Requests()
	if i < 0 || i >= len(raw) {
		t.Fatalf("request %d missing (got %d)", i, len(raw))
	}
	return string(raw[i])
}

// ConfigDir is the child's config directory: the root the loaders derive from
// HOME/XDG on Unix and from APPDATA/USERPROFILE on Windows, so it must follow
// the same per-OS layout the child uses (skills live under it). The config
// file itself is passed explicitly with --config.
func (e *Env) ConfigDir() string { return spillConfigDir(e.home) }

// Path joins rel under the temp workspace.
func (e *Env) Path(rel string) string { return filepath.Join(e.Workdir, rel) }

// WriteFile creates a workspace file (mode 0600) and returns its path.
func (e *Env) WriteFile(rel, content string) string {
	e.t.Helper()
	p := e.Path(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		e.t.Fatal(err)
	}
	return p
}

// ReadFile reads a workspace file.
func (e *Env) ReadFile(rel string) string {
	e.t.Helper()
	root, err := os.OpenRoot(e.Workdir)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() {
		if cerr := root.Close(); cerr != nil {
			e.t.Errorf("close workspace root: %v", cerr)
		}
	}()
	b, err := root.ReadFile(rel)
	if err != nil {
		e.t.Fatal(err)
	}
	return string(b)
}

// ToolCallReply scripts one assistant turn that calls a tool.
func ToolCallReply(id, name, args string) providertest.Reply {
	frames := []string{
		fmt.Sprintf(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":%q,"function":{"name":%q,"arguments":%q}}]}}]}`, id, name, args),
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
		"[DONE]",
	}
	return providertest.Reply{Frames: frames}
}

// CarrierToolReply scripts a tool call carrying an opaque thinking signature.
func CarrierToolReply(signature, id, name, args string) providertest.Reply {
	reasoning := fmt.Sprintf(`[{"type":"thinking","thinking":"plan","signature":%q}]`, signature)
	frames := []string{
		fmt.Sprintf(`{"choices":[{"index":0,"delta":{"reasoning":%s,"tool_calls":[{"index":0,"id":%q,"function":{"name":%q,"arguments":%q}}]}}]}`, reasoning, id, name, args),
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
		"[DONE]",
	}
	return providertest.Reply{Frames: frames}
}

// Contains reports whether the wire body of request i contains needle.
func (e *Env) Contains(t *testing.T, i int, needle string) bool {
	t.Helper()
	return strings.Contains(e.RequestText(t, i), needle)
}
