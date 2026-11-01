package backends

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/damnbot/damnbot/internal/exec"
)

// SingularityConfig is the composition-root view of
// [exec.backends.singularity].
type SingularityConfig struct {
	Binary         string
	Image          string
	Workspace      string
	Network        string // none | host
	Fakeroot       bool
	EnvAllow       []string
	AllowUnlimited bool
}

// Singularity runs commands through `apptainer|singularity exec`; it is a
// container process, not a daemon: killing the process group is the cleanup.
type Singularity struct {
	cfg SingularityConfig

	mu     sync.Mutex
	closed bool
}

var _ exec.Backend = (*Singularity)(nil)

// NewSingularity validates the image/binary and canonical workspace.
func NewSingularity(cfg *SingularityConfig) (*Singularity, error) {
	switch cfg.Network {
	case "", "none":
		cfg.Network = "none"
	case "host":
	default:
		return nil, fmt.Errorf("%w: singularity network %q", exec.ErrSpec, cfg.Network)
	}
	if cfg.Image == "" || strings.ContainsAny(cfg.Image, " \t\r\n\x00") {
		return nil, fmt.Errorf("%w: singularity image is required", exec.ErrSpec)
	}
	root, err := secureRoot(cfg.Workspace)
	if err != nil {
		return nil, err
	}
	cfg.Workspace = root
	return &Singularity{cfg: *cfg}, nil
}

// ID is the backend id.
func (s *Singularity) ID() string { return "singularity" }

// Caps reports that the CLI runs in a local process group (signals) and bind-mounts the
// workspace; CPU/memory/PID limits are not enforced by apptainer.
func (s *Singularity) Caps() exec.Caps {
	return exec.Caps{Signals: true, SharesFS: true, Network: true}
}

// Run executes one command inside the image.
func (s *Singularity) Run(ctx context.Context, spec *exec.Spec) (exec.Result, error) {
	if s.closedNow() {
		return exec.Result{Status: exec.StatusSpawnFailed}, fmt.Errorf("%w: singularity backend closed", exec.ErrUnavailable)
	}
	if spec.PTY {
		return exec.Result{Status: exec.StatusSpawnFailed}, fmt.Errorf("%w: singularity: pty", exec.ErrCapability)
	}
	if spec.Limits.Set() && !s.cfg.AllowUnlimited {
		return exec.Result{Status: exec.StatusSpawnFailed}, fmt.Errorf("%w: singularity: resource_limits", exec.ErrCapability)
	}
	cwd, err := insideRoot(s.cfg.Workspace, spec.Dir)
	if err != nil {
		return exec.Result{Status: exec.StatusSpawnFailed}, err
	}
	env, err := envList(spec.Env, s.cfg.EnvAllow)
	if err != nil {
		return exec.Result{Status: exec.StatusSpawnFailed}, err
	}
	bin, err := resolveBinary(s.cfg.Binary)
	if err != nil {
		return exec.Result{Status: exec.StatusSpawnFailed}, err
	}
	argv := []string{
		bin, "exec", "--containall", "--no-home", "--cleanenv",
		"--pwd", cwd, "--bind", s.cfg.Workspace + ":" + s.cfg.Workspace,
	}
	if s.cfg.Network == "none" {
		argv = append(argv, "--net", "--network", "none")
	}
	if s.cfg.Fakeroot {
		argv = append(argv, "--fakeroot")
	}
	argv = append(argv, s.cfg.Image)
	argv = append(argv, spec.Argv...)
	return runCLI(ctx, s.ID(), &runSpec{
		argv: argv, dir: s.cfg.Workspace, env: env, stdin: spec.Stdin,
		timeout: spec.Timeout, grace: spec.KillGrace, onOutput: spec.OnOutput,
	})
}

// Sweep has no remote state: the command lives in this process group.
func (s *Singularity) Sweep(context.Context, string) (int, error) { return 0, nil }

// Close marks the backend closed; running commands already own their tree.
func (s *Singularity) Close(context.Context) error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}

func (s *Singularity) closedNow() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}
