// Package code executes a short untrusted program in a temporary directory
// through an exec.Backend (SPEC §9.2 execute_code, ADR-089). The runner
// never interprets code itself: it writes one file, starts the language's
// interpreter with a JSON stdin, and enforces the call timeout. Network
// access is deny-only and fails closed when the backend cannot isolate it.
package code

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/exec"
)

// Limits and bounds of one call (SPEC §9.2: 30 s, 512 MiB).
const (
	MaxCodeBytes   = 1 << 20
	DefaultTimeout = 30 * time.Second
	MaxTimeout     = 30 * time.Second
	MemoryMiB      = 512
)

// Language is one supported interpreter.
type Language string

// Closed language set of the tool schema.
const (
	Python     Language = "python"
	JavaScript Language = "javascript"
	Go         Language = "go"
)

// Errors.
var (
	ErrLanguage    = errors.New("exec/code: unsupported language")
	ErrCodeSize    = errors.New("exec/code: code too large")
	ErrNetworkDeny = errors.New("exec/code: network deny is not enforceable")
	ErrBackend     = errors.New("exec/code: backend unavailable")
	// ErrDenied: the approval decision forbids execution (0 spawn).
	ErrDenied = errors.New("exec/code: denied")
)

// Request is one execute_code call after approval.
type Request struct {
	Language Language
	Code     string
	// Stdin is the JSON payload the program reads from stdin (nil: "{}").
	Stdin   []byte
	Timeout time.Duration // 0: DefaultTimeout; clamped to MaxTimeout
	Network string        // "" or "deny"; any other value is refused
}

// Result is the observable outcome.
type Result struct {
	Status    exec.Status
	ExitCode  *int
	Stdout    []byte // combined backend output (already capped by exec)
	Truncated bool
	Duration  time.Duration
	Backend   string
	// Isolation states what the backend really enforced (SPEC §14.2 Meta).
	Isolation Isolation
}

// Isolation is the honest enforcement report of one call.
type Isolation struct {
	Network string // "deny" | "none"
	Memory  string // "rlimit" | "none"
}

// Config wires the runner.
type Config struct {
	Backend exec.Backend // required
	// Env is the complete child environment (allowlist built by the caller);
	// nil means a minimal PATH only.
	Env []string
	// Root is the parent of the per-call temp dir ("" = os.TempDir()).
	Root string
}

// Runner executes code through one backend.
type Runner struct{ cfg Config }

// New returns a Runner; a nil backend is rejected (fail closed).
func New(cfg Config) (*Runner, error) {
	if cfg.Backend == nil {
		return nil, fmt.Errorf("%w: nil backend", ErrBackend)
	}
	return &Runner{cfg: cfg}, nil
}

// interpreter returns the argv prefix and file name of one language.
func interpreter(lang Language) (argv []string, file string, err error) {
	switch lang {
	case Python:
		return []string{"python3", "-I", "-B", "main.py"}, "main.py", nil
	case JavaScript:
		return []string{"node", "--no-warnings", "main.js"}, "main.js", nil
	case Go:
		return []string{"go", "run", "main.go"}, "main.go", nil
	default:
		return nil, "", fmt.Errorf("%w: %q", ErrLanguage, lang)
	}
}

// Run executes req. It refuses before spawning when the backend cannot
// enforce the requested network policy (ADR-076, GAP-SEC-23).
func (r *Runner) Run(ctx context.Context, req *Request) (Result, error) {
	if len(req.Code) > MaxCodeBytes {
		return Result{}, fmt.Errorf("%w: %d > %d", ErrCodeSize, len(req.Code), MaxCodeBytes)
	}
	argv, file, err := interpreter(req.Language)
	if err != nil {
		return Result{}, err
	}
	switch req.Network {
	case "", "deny":
	default:
		return Result{}, fmt.Errorf("%w: network %q", exec.ErrSpec, req.Network)
	}
	caps := r.cfg.Backend.Caps()
	if !caps.Network {
		return Result{Backend: r.cfg.Backend.ID()}, fmt.Errorf("%w: %s", ErrNetworkDeny, r.cfg.Backend.ID())
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if timeout > MaxTimeout {
		timeout = MaxTimeout
	}
	dir, err := os.MkdirTemp(r.cfg.Root, "damnbot-code-*")
	if err != nil {
		return Result{}, fmt.Errorf("%w: temp dir: %w", ErrBackend, err)
	}
	//nolint:errcheck,gosec // best-effort temp cleanup
	defer func() { _ = os.RemoveAll(dir) }()
	//nolint:gosec // the directory needs owner execute; only the owner can enter
	if cerr := os.Chmod(dir, 0o700); cerr != nil {
		return Result{}, fmt.Errorf("%w: chmod: %w", ErrBackend, cerr)
	}
	argv, err = resolve(argv, r.cfg.Env)
	if err != nil {
		return Result{}, err
	}
	//nolint:gosec // dir is our 0700 temp dir and file is a fixed name
	if err := os.WriteFile(filepath.Join(dir, file), []byte(req.Code), 0o600); err != nil {
		return Result{}, fmt.Errorf("%w: write: %w", ErrBackend, err)
	}
	stdin := req.Stdin
	if stdin == nil {
		stdin = []byte("{}")
	}
	spec := &exec.Spec{
		Argv: argv, Dir: dir, Env: env(r.cfg.Env), Stdin: stdin,
		Timeout: timeout, IdleTimeout: timeout, KillGrace: exec.DefaultKillGrace,
	}
	if caps.ResourceLimits {
		spec.Limits = exec.Limits{MemoryMiB: MemoryMiB}
	}
	res, runErr := r.cfg.Backend.Run(ctx, spec)
	out := Result{
		Status: res.Status, ExitCode: res.ExitCode, Stdout: res.Output,
		Truncated: res.Truncated, Duration: res.Duration, Backend: r.cfg.Backend.ID(),
		Isolation: Isolation{Network: "deny", Memory: "none"},
	}
	if caps.ResourceLimits {
		out.Isolation.Memory = "rlimit"
	}
	if runErr != nil {
		return out, fmt.Errorf("%w: %w", ErrBackend, runErr)
	}
	return out, nil
}

// resolve makes argv[0] absolute through the child environment's PATH (the
// exec.Spec contract); a missing interpreter fails closed before the write.
func resolve(argv, env []string) ([]string, error) {
	name := argv[0]
	if strings.ContainsRune(name, os.PathSeparator) {
		return argv, nil
	}
	path := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = v
			break
		}
	}
	if path == "" {
		path = "/usr/local/bin:/usr/bin:/bin"
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			continue
		}
		cand := filepath.Join(dir, name)
		//nolint:gosec // PATH resolution is the interpreter contract
		if info, err := os.Stat(cand); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			out := append([]string(nil), argv...)
			out[0] = cand
			return out, nil
		}
	}
	return nil, fmt.Errorf("%w: interpreter %s not found", ErrLanguage, name)
}

// env returns the child environment; nil becomes a fixed minimal PATH so the
// interpreter is found without inheriting the parent environment.
func env(in []string) []string {
	if len(in) > 0 {
		return in
	}
	return []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/nonexistent", "TMPDIR=/tmp"}
}

// Authorize turns a policy decision into a spawn gate: only an ask/auto
// decision may reach Run, and a deny never starts a process.
func Authorize(d Decision) error {
	if d.Requirement == RequireDeny {
		return fmt.Errorf("%w: %s", ErrDenied, d.Reason)
	}
	return nil
}

// Preview is the approval preview of one call (ADR-089: language, summary,
// network). The code itself is summarized, never echoed in full.
func Preview(req *Request) string {
	var b strings.Builder
	fmt.Fprintf(&b, "language: %s\nnetwork: deny\n", req.Language)
	fmt.Fprintf(&b, "code: %d bytes, %d lines\n", len(req.Code), strings.Count(req.Code, "\n")+1)
	return b.String()
}
