//go:build !minimal

package backends

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/damnbot/damnbot/internal/exec"
)

// DockerConfig is the composition-root view of [exec.backends.docker].
type DockerConfig struct {
	Binary         string
	Image          string
	User           string
	Workspace      string // the only host path mounted (read-write)
	CPUMilli       int
	MemoryMiB      int
	PIDs           int
	Network        string // none | bridge | allowlist
	NetworkName    string // attach network for allowlist (operator: no default route)
	EgressAllow    []string
	ProxyURL       string
	EnvAllow       []string
	AllowUnlimited bool
}

// Docker runs commands in a throwaway container via the docker CLI.
// The image must exist locally (`--pull=never`): a missing image fails
// closed instead of reaching the network.
type Docker struct {
	cfg  DockerConfig
	root string // canonical workspace root

	mu     sync.Mutex
	names  map[string]bool
	closed bool
}

// Compile-time proof of the backend contract.
var _ exec.Backend = (*Docker)(nil)

// NewDocker validates the workspace and returns the backend. It does not
// touch the daemon (P16 lazy).
func NewDocker(cfg *DockerConfig) (*Docker, error) {
	switch cfg.Network {
	case "", "none":
		cfg.Network = "none"
	case "bridge":
	case "allowlist":
		if len(cfg.EgressAllow) == 0 || cfg.ProxyURL == "" || cfg.NetworkName == "" {
			return nil, fmt.Errorf("%w: docker network allowlist needs egress_allow, proxy_url and network_name", exec.ErrCapability)
		}
	default:
		return nil, fmt.Errorf("%w: docker network %q", exec.ErrSpec, cfg.Network)
	}
	if cfg.Image == "" || strings.ContainsAny(cfg.Image, " \t\r\n\x00") {
		return nil, fmt.Errorf("%w: docker image is required", exec.ErrSpec)
	}
	if cfg.User == "" || strings.HasPrefix(cfg.User, "0") || cfg.User == "root" {
		return nil, fmt.Errorf("%w: docker user must be non-root", exec.ErrSpec)
	}
	root, err := secureRoot(cfg.Workspace)
	if err != nil {
		return nil, err
	}
	cfg.Workspace = root
	return &Docker{cfg: *cfg, root: root, names: map[string]bool{}}, nil
}

// ID is the backend id.
func (d *Docker) ID() string { return "docker" }

// Caps reports that signals, resource limits, network isolation and the shared
// workspace are enforced; PTY and persistent sessions are not.
func (d *Docker) Caps() exec.Caps {
	return exec.Caps{Signals: true, SharesFS: true, ResourceLimits: true, Network: true}
}

// Run starts one container named after the exec id and removes it
// afterwards; the host workspace is mounted at the same path.
func (d *Docker) Run(ctx context.Context, spec *exec.Spec) (exec.Result, error) {
	if err := d.ready(); err != nil {
		return exec.Result{Status: exec.StatusSpawnFailed}, err
	}
	if spec.PTY {
		return exec.Result{Status: exec.StatusSpawnFailed}, fmt.Errorf("%w: docker: pty", exec.ErrCapability)
	}
	cwd, err := insideRoot(d.root, spec.Dir)
	if err != nil {
		return exec.Result{Status: exec.StatusSpawnFailed}, err
	}
	env, err := EnvList(spec.Env, d.cfg.EnvAllow)
	if err != nil {
		return exec.Result{Status: exec.StatusSpawnFailed}, err
	}
	id := exec.NewExecID()
	name := "damnbot-" + id
	argv, envFile, err := d.argv(id, name, cwd, spec, env)
	if err != nil {
		return exec.Result{Status: exec.StatusSpawnFailed}, err
	}
	d.track(name)
	cleanup := func(cctx context.Context) error {
		d.untrack(name)
		if envFile != "" {
			removeTemp(envFile)
		}
		return d.remove(cctx, name)
	}
	res, rerr := RunCLI(ctx, d.ID(), &CLISpec{
		argv: argv, dir: d.root, timeout: spec.Timeout, grace: spec.KillGrace,
		stdin: spec.Stdin, onOutput: spec.OnOutput, cleanup: cleanup,
	})
	if envFile != "" {
		removeTemp(envFile)
	}
	res.Execution = sandboxExecution(d.ID(), "container replaces host execution")
	return res, rerr
}

// ready fails closed when the CLI is missing or the daemon is unreachable.
func (d *Docker) ready() error {
	if _, err := ResolveBinary(d.cfg.Binary); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return fmt.Errorf("%w: docker backend closed", exec.ErrUnavailable)
	}
	return nil
}

// argv builds `docker run` for one call. The workspace mount is the only
// bind mount; docker.sock is never mounted and such a workspace is refused.
func (d *Docker) argv(id, name, cwd string, spec *exec.Spec, env []string) ([]string, string, error) {
	mount, merr := bindMount(d.root)
	if merr != nil {
		return nil, "", merr
	}
	workdir, werr := containerPath(cwd)
	if werr != nil {
		return nil, "", werr
	}
	argv := []string{
		d.cfg.Binary, "run", "--rm", "-i",
		"--name", name,
		"--label", "damnbot.exec_id=" + id,
		"--label", "damnbot.backend=docker",
		"--network=" + d.networkMode(),
		"--pull=never",
		"--cap-drop=ALL",
		"--security-opt=no-new-privileges",
		"--user", d.cfg.User,
		"--mount", mount,
		"--workdir", workdir,
	}
	argv = append(argv, d.limitArgs(spec.Limits)...)
	envFile := ""
	if len(env) > 0 {
		var err error
		envFile, err = writeEnvFile(env)
		if err != nil {
			return nil, "", err
		}
		argv = append(argv, "--env-file", envFile)
	}
	if d.cfg.Network == "allowlist" {
		// The container has no route of its own; the filtering proxy is
		// the only egress and enforces EgressAllow.
		argv = append(argv, "-e", "HTTP_PROXY="+d.cfg.ProxyURL, "-e", "HTTPS_PROXY="+d.cfg.ProxyURL,
			"-e", "NO_PROXY=127.0.0.1,localhost")
	}
	argv = append(argv, d.cfg.Image)
	return append(argv, spec.Argv...), envFile, nil
}

// networkMode is the docker network attachment: none/bridge, or the
// operator-provided isolated network of allowlist mode (a container can only
// reach the filtering proxy that shares that network).
func (d *Docker) networkMode() string {
	if d.cfg.Network == "allowlist" {
		return d.cfg.NetworkName
	}
	return d.cfg.Network
}

// limitArgs is the cgroup cap set of one call; an explicit per-call limit
// wins over the configured default.
func (d *Docker) limitArgs(limits exec.Limits) []string {
	if d.cfg.AllowUnlimited {
		return nil
	}
	if !limits.Set() {
		limits = exec.Limits{CPUMilli: d.cfg.CPUMilli, MemoryMiB: d.cfg.MemoryMiB, PIDs: d.cfg.PIDs}
	}
	var args []string
	if limits.CPUMilli > 0 {
		args = append(args, "--cpus="+strconv.FormatFloat(float64(limits.CPUMilli)/1000, 'f', 3, 64))
	}
	if limits.MemoryMiB > 0 {
		args = append(args, "--memory="+strconv.Itoa(limits.MemoryMiB)+"m")
	}
	if limits.PIDs > 0 {
		args = append(args, "--pids-limit="+strconv.Itoa(limits.PIDs))
	}
	return args
}

// remove deletes the container; an absent container is not an error.
func (d *Docker) remove(ctx context.Context, name string) error {
	bin, err := ResolveBinary(d.cfg.Binary)
	if err != nil {
		return err
	}
	if _, err := RunCLI(ctx, d.ID(), &CLISpec{argv: []string{bin, "rm", "-f", name}, timeout: exec.DefaultTimeout}); err != nil {
		return err
	}
	return nil
}

func (d *Docker) track(name string) {
	d.mu.Lock()
	d.names[name] = true
	d.mu.Unlock()
}

func (d *Docker) untrack(name string) {
	d.mu.Lock()
	delete(d.names, name)
	d.mu.Unlock()
}

// Sweep removes the container of execID (0 orphan). It reports 1 when a
// container was removed.
func (d *Docker) Sweep(ctx context.Context, execID string) (int, error) {
	if execID == "" {
		return 0, fmt.Errorf("%w: empty exec id", exec.ErrSpec)
	}
	name := "damnbot-" + execID
	if err := d.remove(ctx, name); err != nil {
		return 0, err
	}
	return 1, nil
}

// Close removes every container this backend still owns.
func (d *Docker) Close(ctx context.Context) error {
	d.mu.Lock()
	d.closed = true
	names := make([]string, 0, len(d.names))
	for name := range d.names {
		names = append(names, name)
	}
	d.mu.Unlock()
	var errs error
	for _, name := range names {
		if err := d.remove(ctx, name); err != nil {
			errs = errors.Join(errs, err)
			continue
		}
		d.untrack(name)
	}
	return errs
}

// secureRoot canonicalizes the workspace and refuses the paths whose mount
// would hand the container the host control plane.
func secureRoot(workspace string) (string, error) {
	if workspace == "" {
		return "", fmt.Errorf("%w: docker workspace is required", exec.ErrSpec)
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return "", fmt.Errorf("%w: %w", exec.ErrSpec, err)
	}
	root, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("%w: workspace: %w", exec.ErrSpec, err)
	}
	switch root {
	case "/", "/var/run", "/run", "/var/run/docker.sock":
		return "", fmt.Errorf("%w: refusing to mount %s", exec.ErrCapability, root)
	}
	for _, banned := range []string{"/var/run/docker.sock", "/run/docker.sock"} {
		if root == banned || strings.HasPrefix(root, banned+"/") {
			return "", fmt.Errorf("%w: refusing to mount the container socket", exec.ErrCapability)
		}
	}
	if st, err := os.Lstat(filepath.Join(root, "docker.sock")); err == nil && st.Mode()&os.ModeSocket != 0 {
		return "", fmt.Errorf("%w: workspace contains docker.sock", exec.ErrCapability)
	}
	return root, nil
}

// insideRoot resolves dir under root and rejects traversal and symlink
// escapes.
func insideRoot(root, dir string) (string, error) {
	if dir == "" {
		return root, nil
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("%w: cwd: %w", exec.ErrSpec, err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("%w: cwd: %w", exec.ErrSpec, err)
	}
	rel, err := filepath.Rel(root, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: cwd %s is outside the mounted workspace %s", exec.ErrCapability, real, root)
	}
	return real, nil
}

// removeTemp discards a temporary file; absence is not a failure.
func removeTemp(path string) {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return // best effort: a stale temp file is not actionable here
	}
}

// writeEnvFile writes a 0600 file (values never appear in `ps`).
func writeEnvFile(env []string) (string, error) {
	f, err := os.CreateTemp("", "damnbot-exec-env-*")
	if err != nil {
		return "", fmt.Errorf("%w: env file: %w", exec.ErrUnavailable, err)
	}
	name := f.Name()
	var errs error
	for _, kv := range env {
		if _, err := f.WriteString(kv + "\n"); err != nil {
			errs = errors.Join(errs, err)
		}
	}
	errs = errors.Join(errs, f.Close(), os.Chmod(name, 0o600))
	if errs != nil {
		removeTemp(name)
		return "", fmt.Errorf("%w: env file: %w", exec.ErrUnavailable, errs)
	}
	return name, nil
}
