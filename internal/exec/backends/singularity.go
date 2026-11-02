//go:build !minimal

package backends

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/damnbot/damnbot/internal/exec"
)

// SingularityConfig is the composition-root view of [exec.backends.singularity].
type SingularityConfig struct {
	Binary, Image, Workspace, Network, NetworkName, ProxyURL string
	EgressAllow, EnvAllow                                    []string
	CPUMilli, MemoryMiB, PIDs                                int
	AllowUnlimited                                           bool
}

// Singularity runs commands through the apptainer/singularity CLI.
type Singularity struct {
	cfg  SingularityConfig
	root string

	probe    sync.Once
	probeErr error
}

var _ exec.Backend = (*Singularity)(nil)

// NewSingularity validates the workspace; it runs no process until Run (P16).
func NewSingularity(cfg *SingularityConfig) (*Singularity, error) {
	switch cfg.Network {
	case "", "none":
		cfg.Network = "none"
	case "bridge":
	case "allowlist":
		if len(cfg.EgressAllow) == 0 || cfg.ProxyURL == "" || cfg.NetworkName == "" {
			return nil, fmt.Errorf("%w: singularity allowlist needs egress_allow, proxy_url and network_name", exec.ErrCapability)
		}
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
	if strings.ContainsAny(root, ":,") {
		return nil, fmt.Errorf("%w: workspace %q cannot be a bind source", exec.ErrSpec, root)
	}
	cfg.Workspace = root
	return &Singularity{cfg: *cfg, root: root}, nil
}

// ID is the backend id.
func (s *Singularity) ID() string { return "singularity" }

// Caps reports signals, the bind mount, cgroup limits and the netns.
func (s *Singularity) Caps() exec.Caps {
	return exec.Caps{Signals: true, SharesFS: true, ResourceLimits: true, Network: true}
}

// Run starts one container; the process group is killed within the deadline.
func (s *Singularity) Run(ctx context.Context, spec *exec.Spec) (exec.Result, error) {
	if err := s.ensureLimits(ctx); err != nil {
		return exec.Result{Status: exec.StatusSpawnFailed}, err
	}
	if spec.PTY {
		return exec.Result{Status: exec.StatusSpawnFailed}, fmt.Errorf("%w: singularity: pty", exec.ErrCapability)
	}
	cwd, err := insideRoot(s.root, spec.Dir)
	if err != nil {
		return exec.Result{Status: exec.StatusSpawnFailed}, err
	}
	env, err := EnvList(spec.Env, s.cfg.EnvAllow)
	if err != nil {
		return exec.Result{Status: exec.StatusSpawnFailed}, err
	}
	id := exec.NewExecID()
	res, rerr := RunCLI(ctx, s.ID(), &CLISpec{
		argv: s.argv(cwd, spec), dir: s.root, env: s.childEnv(id, env),
		stdin: spec.Stdin, timeout: spec.Timeout, grace: spec.KillGrace,
	})
	res.ExecID = id
	return res, rerr
}

func (s *Singularity) argv(cwd string, spec *exec.Spec) []string {
	argv := []string{s.cfg.Binary, "exec", "--containall", "--cleanenv", "--bind", s.root + ":" + s.root, "--pwd", cwd}
	switch s.cfg.Network {
	case "none":
		argv = append(argv, "--net", "--network", "none")
	case "allowlist":
		argv = append(argv, "--net", "--network", s.cfg.NetworkName)
	}
	l := spec.Limits
	if !l.Set() {
		l = exec.Limits{CPUMilli: s.cfg.CPUMilli, MemoryMiB: s.cfg.MemoryMiB, PIDs: s.cfg.PIDs}
	}
	if !s.cfg.AllowUnlimited {
		if l.CPUMilli > 0 {
			argv = append(argv, "--cpus", strconv.FormatFloat(float64(l.CPUMilli)/1000, 'f', 3, 64))
		}
		if l.MemoryMiB > 0 {
			argv = append(argv, "--memory", strconv.Itoa(l.MemoryMiB)+"M")
		}
		if l.PIDs > 0 {
			argv = append(argv, "--pids-limit", strconv.Itoa(l.PIDs))
		}
	}
	return append(append(argv, s.cfg.Image), spec.Argv...)
}

func (s *Singularity) childEnv(id string, env []string) []string {
	out := make([]string, 0, len(env)+8)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "APPTAINERENV_") && !strings.HasPrefix(kv, "SINGULARITYENV_") {
			out = append(out, kv)
		}
	}
	out = append(out, exec.MarkerEnv+"="+id, "DAMNBOT=1", "APPTAINERENV_"+exec.MarkerEnv+"="+id)
	for _, kv := range env {
		out = append(out, "APPTAINERENV_"+kv)
	}
	if s.cfg.Network == "allowlist" {
		out = append(out, "APPTAINERENV_HTTP_PROXY="+s.cfg.ProxyURL, "APPTAINERENV_HTTPS_PROXY="+s.cfg.ProxyURL,
			"APPTAINERENV_NO_PROXY=127.0.0.1,localhost")
	}
	return out
}

// ensureLimits verifies cgroup delegation before a real command runs. A host
// without delegated CPU/memory/PID controllers fails closed with
// ErrCapability instead of starting a container without limits.
func (s *Singularity) ensureLimits(ctx context.Context) error {
	limits := exec.Limits{CPUMilli: s.cfg.CPUMilli, MemoryMiB: s.cfg.MemoryMiB, PIDs: s.cfg.PIDs}
	if s.cfg.AllowUnlimited || !limits.Set() {
		return nil
	}
	s.probe.Do(func() { s.probeErr = s.probeLimits(ctx) })
	return s.probeErr
}

// probeLimits asks apptainer whether cgroups are available. The probe is a
// read-only capability query, never a user command whose exit code could be
// mistaken for the command's own failure.
func (s *Singularity) probeLimits(ctx context.Context) error {
	bin, err := ResolveBinary(s.cfg.Binary)
	if err != nil {
		return err
	}
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	res, err := RunCLI(probeCtx, s.ID(), &CLISpec{
		argv: []string{bin, "cgroup", "status"}, dir: s.root, timeout: 10 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("%w: singularity cgroup probe: %w", exec.ErrCapability, err)
	}
	if res.Status != exec.StatusExited || res.ExitCode == nil || *res.ExitCode != 0 ||
		!strings.Contains(strings.ToLower(string(res.Output)), "cgroup") {
		return fmt.Errorf("%w: singularity: no_resource_limits: %s", exec.ErrCapability,
			strings.TrimSpace(string(res.Output)))
	}
	return nil
}

// Sweep kills the marked processes of execID (shared with local).
func (s *Singularity) Sweep(ctx context.Context, execID string) (int, error) {
	n, err := exec.SweepMarker(ctx, execID)
	if err != nil {
		return 0, fmt.Errorf("singularity sweep: %w", err)
	}
	return n, nil
}

// Close has nothing to release: every Run kills its own process group.
func (s *Singularity) Close(context.Context) error { return nil }
