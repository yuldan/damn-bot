package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// verifyRecipe is the run recipe one project declares (CLI-033/OPS-025). Argv
// is never a shell string: the detector composes argv from literals, so a
// manifest can name an argument but can never inject shell syntax.
type verifyRecipe struct {
	Kind    string `json:"kind"`
	File    string `json:"file"`
	Command string `json:"command"`
	argv    []string
}

// verifyRun is the outcome of --run.
type verifyRun struct {
	Ran        bool  `json:"ran"`
	OK         bool  `json:"ok"`
	ExitCode   int   `json:"exit_code"`
	DurationMS int64 `json:"duration_ms"`
	TimedOut   bool  `json:"timed_out,omitempty"`
}

// verifyReport is the --json document. Recipe is nil when no manifest matched.
type verifyReport struct {
	Path    string        `json:"path"`
	Found   bool          `json:"found"`
	Recipe  *verifyRecipe `json:"recipe,omitempty"`
	Run     *verifyRun    `json:"run,omitempty"`
	Refused string        `json:"refused,omitempty"`
}

func verifyUsage(w io.Writer) {
	say(w, "usage: damnbot verify [--path DIR] [--config FILE] [--run] [--timeout 2m] [--json]\n")
}

// runVerify implements `damnbot verify` (CLI-033, OPS-025): detect a project's
// run recipe and, only with --run, smoke-test it. Detection is read-only.
func runVerify(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("path", ".", "project directory to inspect")
	configPath := fs.String("config", "", "config file (default: the active profile)")
	run := fs.Bool("run", false, "execute the detected recipe as a smoke test")
	timeout := fs.Duration("timeout", 2*time.Minute, "smoke-test timeout")
	asJSON := fs.Bool("json", false, "machine-readable report")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		say(stderr, "verify: unexpected argument %q\n", cleanArg(fs.Arg(0)))
		return 2
	}
	if *timeout <= 0 {
		say(stderr, "verify: --timeout must be positive\n")
		return 2
	}
	root, err := verifyRoot(*path)
	if err != nil {
		say(stderr, "verify: %s\n", cleanLine(err.Error()))
		return 1
	}
	recipe, found := detectVerifyRecipe(root)
	rep := verifyReport{Path: root, Found: found}
	if found {
		rep.Recipe = &recipe
	}
	if !found {
		if *asJSON {
			return writeVerifyJSON(stdout, rep)
		}
		say(stderr, "verify: no run recipe found in %s\n", root)
		return 1
	}
	return verifyDispatch(recipe, rep, *run, *timeout, *configPath, *asJSON, stdout, stderr)
}

// verifyDispatch owns the run/refuse decision so runVerify stays within the
// cognitive-complexity gate. The trust check runs before any execution.
func verifyDispatch(recipe verifyRecipe, rep verifyReport, wantRun bool, timeout time.Duration, configPath string, asJSON bool, stdout, stderr io.Writer) int {
	if !wantRun {
		if asJSON {
			return writeVerifyJSON(stdout, rep)
		}
		verifyHuman(stdout, rep, 0, false)
		return 0
	}
	trusted, terr := verifyWorkspaceTrust(configPath, rep.Path)
	if terr != nil {
		say(stderr, "verify: trust: %s\n", cleanLine(terr.Error()))
		return 1
	}
	if !trusted {
		rep.Refused = "untrusted workspace"
		if asJSON {
			return writeVerifyJSON(stdout, rep)
		}
		say(stderr, "verify: %s is not a trusted workspace; --run refused (register it with `damnbot skills trust`)\n", rep.Path)
		return 1
	}
	result, rerr := executeVerifyRecipe(rep.Path, recipe, timeout, stdout, stderr)
	rep.Run = &result
	if asJSON {
		code := writeVerifyJSON(stdout, rep)
		if rerr != nil || !result.OK {
			return 1
		}
		return code
	}
	if rerr != nil {
		say(stderr, "verify: %s\n", cleanLine(rerr.Error()))
		return 1
	}
	verifyHuman(stdout, rep, result.DurationMS, true)
	if !result.OK {
		return 1
	}
	return 0
}

// verifyRoot resolves and validates the project directory. A symlink is
// followed (the operator named it), but the result must be a directory.
func verifyRoot(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("path: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("path %s: %w", path, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("path %s is not a directory", path)
	}
	return filepath.Clean(abs), nil
}

// verifyWorkspaceTrust applies the same decision as `run`: a recipe executes
// project code, so an unregistered workspace is refused before any child
// process starts. Detection alone never calls this; reading manifests is safe
// in an untrusted checkout.
func verifyWorkspaceTrust(configPath, root string) (bool, error) {
	o := runOptions{configPath: configPath}
	cfg, _, configDir, home, err := loadRunEnv(&o)
	if err != nil {
		return false, fmt.Errorf("config: %w", err)
	}
	trusted, err := workspaceTrusted(&cfg, configDir, root, home)
	if err != nil {
		return false, fmt.Errorf("workspace: %w", err)
	}
	return trusted, nil
}

func verifyHuman(w io.Writer, rep verifyReport, durationMS int64, ran bool) {
	say(w, "verify: %s\n", rep.Path)
	if rep.Recipe != nil {
		say(w, "recipe: %s (%s)\n", rep.Recipe.Kind, rep.Recipe.File)
		say(w, "command: %s\n", rep.Recipe.Command)
	}
	switch {
	case !ran:
		say(w, "smoke: not run (pass --run)\n")
	case rep.Run != nil && rep.Run.TimedOut:
		say(w, "smoke: timeout after %s\n", durationString(durationMS))
	case rep.Run != nil && rep.Run.OK:
		say(w, "smoke: pass (%s)\n", durationString(durationMS))
	default:
		code := 1
		if rep.Run != nil {
			code = rep.Run.ExitCode
		}
		say(w, "smoke: fail (exit %d, %s)\n", code, durationString(durationMS))
	}
}

func durationString(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	if d < time.Second {
		return fmt.Sprintf("%dms", ms)
	}
	return d.Truncate(10 * time.Millisecond).String()
}

func writeVerifyJSON(w io.Writer, rep verifyReport) int {
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return 1
	}
	return say(w, "%s\n", b)
}

// executeVerifyRecipe runs one argv recipe without a shell. --run is the
// explicit opt-in; the child inherits the operator environment and working
// directory, and its output streams to the terminal.
func executeVerifyRecipe(root string, r verifyRecipe, timeout time.Duration, stdout, stderr io.Writer) (verifyRun, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	start := time.Now()
	// #nosec G204 G702 -- the recipe argv is built from detector literals; the
	// operator opted in with --run and the project controls only its own test.
	cmd := exec.CommandContext(ctx, r.argv[0], r.argv[1:]...)
	cmd.Dir = root
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.Env = os.Environ()
	err := cmd.Run()
	run := verifyRun{Ran: true, DurationMS: time.Since(start).Milliseconds()}
	switch {
	case err == nil:
		run.OK = true
		return run, nil
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		run.TimedOut, run.ExitCode = true, -1
		return run, nil
	}
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		run.ExitCode = exitErr.ExitCode()
		return run, nil
	}
	return run, fmt.Errorf("run %s: %w", r.argv[0], err)
}

// detectVerifyRecipe returns the first project recipe in documented priority
// order. Each detector only reads manifests; a manifest that exists but does
// not declare a runnable test falls through to the next candidate.
func detectVerifyRecipe(root string) (verifyRecipe, bool) {
	detectors := []func(string) (verifyRecipe, bool){
		detectGoRecipe,
		detectCargoRecipe,
		detectNodeRecipe,
		detectPythonRecipe,
		detectMakeRecipe,
		detectJustRecipe,
	}
	for _, detect := range detectors {
		if r, ok := detect(root); ok {
			return r, true
		}
	}
	return verifyRecipe{}, false
}

func detectGoRecipe(root string) (verifyRecipe, bool) {
	if !verifyFile(root, "go.mod") {
		return verifyRecipe{}, false
	}
	return verifyRecipe{Kind: "go", File: "go.mod", Command: "go test ./...", argv: []string{"go", "test", "./..."}}, true
}

func detectCargoRecipe(root string) (verifyRecipe, bool) {
	if !verifyFile(root, "Cargo.toml") {
		return verifyRecipe{}, false
	}
	return verifyRecipe{Kind: "cargo", File: "Cargo.toml", Command: "cargo test", argv: []string{"cargo", "test"}}, true
}

// detectNodeRecipe reads package.json.scripts.test, which is the only Node
// declaration that guarantees a real test entry point.
func detectNodeRecipe(root string) (verifyRecipe, bool) {
	path := filepath.Join(root, "package.json")
	if !verifyFile(root, "package.json") {
		return verifyRecipe{}, false
	}
	// #nosec G304 -- manifest under the operator-supplied project directory.
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > 1<<20 {
		return verifyRecipe{}, false
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal(raw, &pkg) != nil || strings.TrimSpace(pkg.Scripts["test"]) == "" {
		return verifyRecipe{}, false
	}
	manager := "npm"
	switch {
	case verifyFile(root, "pnpm-lock.yaml"):
		manager = "pnpm"
	case verifyFile(root, "yarn.lock"):
		manager = "yarn"
	case verifyFile(root, "bun.lockb"), verifyFile(root, "bun.lock"):
		manager = "bun"
	}
	return verifyRecipe{
		Kind: "node", File: "package.json", Command: manager + " test",
		argv: []string{manager, "test"},
	}, true
}

func detectPythonRecipe(root string) (verifyRecipe, bool) {
	file := ""
	switch {
	case verifyFile(root, "pytest.ini"):
		file = "pytest.ini"
	case verifyFile(root, "tox.ini"):
		file = "tox.ini"
	case verifyFile(root, "conftest.py"):
		file = "conftest.py"
	case verifyFile(root, "pyproject.toml") && verifyContains(root, "pyproject.toml", "[tool.pytest"):
		file = "pyproject.toml"
	case verifyPythonTests(root):
		file = "tests/"
	}
	if file == "" {
		return verifyRecipe{}, false
	}
	return verifyRecipe{
		Kind: "pytest", File: file, Command: "python -m pytest -q",
		argv: []string{"python", "-m", "pytest", "-q"},
	}, true
}

// verifyPythonTests reports whether tests/ holds at least one test_*.py file.
func verifyPythonTests(root string) bool {
	entries, err := os.ReadDir(filepath.Join(root, "tests"))
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "test_") && strings.HasSuffix(e.Name(), ".py") {
			return true
		}
	}
	return false
}

// detectMakeRecipe requires a `test:` target, not merely a Makefile.
func detectMakeRecipe(root string) (verifyRecipe, bool) {
	if !verifyTarget(root, "Makefile", "test:") {
		return verifyRecipe{}, false
	}
	return verifyRecipe{Kind: "make", File: "Makefile", Command: "make test", argv: []string{"make", "test"}}, true
}

func detectJustRecipe(root string) (verifyRecipe, bool) {
	for _, name := range []string{"justfile", "Justfile"} {
		if verifyTarget(root, name, "test:") || verifyTarget(root, name, "test ") {
			return verifyRecipe{Kind: "just", File: name, Command: "just test", argv: []string{"just", "test"}}, true
		}
	}
	return verifyRecipe{}, false
}

// verifyTarget reports whether a build manifest declares one target line.
func verifyTarget(root, name, prefix string) bool {
	raw, ok := verifyRead(root, name, 1<<20)
	if !ok {
		return false
	}
	for line := range strings.Lines(string(raw)) {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, prefix) && !strings.HasPrefix(trimmed, "#") {
			return true
		}
	}
	return false
}

func verifyContains(root, name, needle string) bool {
	raw, ok := verifyRead(root, name, 1<<20)
	return ok && strings.Contains(string(raw), needle)
}

func verifyRead(root, name string, limit int64) ([]byte, bool) {
	path := filepath.Join(root, name)
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, false
	}
	// #nosec G304 -- manifest under the operator-supplied project directory.
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	return raw, true
}

func verifyFile(root, name string) bool {
	info, err := os.Stat(filepath.Join(root, name))
	return err == nil && info.Mode().IsRegular()
}
