//go:build !minimal

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"github.com/damnbot/damnbot/internal/computeruse"
	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/modules/host"
)

// Exit codes of `damnbot cua` (SPEC §10); every subcommand uses the same set.
const (
	cuaExitOK          = 0
	cuaExitFail        = 1
	cuaExitUsage       = 2
	cuaExitUnavailable = 3
	cuaExitDenied      = 4
)

// runCua dispatches the one-shot computer-use CLI; it never needs a turn.
func runCua(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		cuaUsage(stderr)
		return cuaExitUsage
	}
	switch args[0] {
	case "status", "start", "stop", "screenshot", "click", "type", "install", "doctor", "permissions", "screen":
		return cuaCommand(args[0], args[1:], stdout, stderr)
	default:
		sessionsPrintf(stderr, "cua: unknown subcommand %q\n", cleanArg(args[0]))
		cuaUsage(stderr)
		return cuaExitUsage
	}
}

func cuaUsage(w io.Writer) {
	sessionsPrintf(w, "usage: damnbot cua status|doctor|permissions|screenshot [flags]\n")
	sessionsPrintf(w, "       damnbot cua install|screen [--yes]\n")
	sessionsPrintf(w, "       damnbot cua start|stop|click|type --yes\n")
	sessionsPrintf(w, "\ncommon flags: --config <path>. install, screen, start, stop and input\n")
	sessionsPrintf(w, "actions need --yes; status, doctor, permissions and screenshot are read-only.\n")
	sessionsPrintf(w, "install stages --source (checksum verified when --sha256 or a .sha256\n")
	sessionsPrintf(w, "sidecar exists) under <data>/cua and never writes elsewhere.\n")
}

// cuaApprovals prints the request preview and allows only this one action when
// --yes was given. It never offers a session-wide grant.
type cuaApprovals struct {
	out io.Writer
	yes bool
}

func (a cuaApprovals) Approve(_ context.Context, req *host.ApprovalRequest) (host.Approval, error) {
	if err := req.Validate(); err != nil {
		return host.ApprovalDeny, fmt.Errorf("cua: approval request: %w", err)
	}
	sessionsPrintf(a.out, "approval: %s (%s)\n%s\n", req.Tool, req.Class, cleanLine(req.Preview))
	if !a.yes {
		return host.ApprovalDeny, nil
	}
	return host.ApprovalAllowOnce, nil
}

// cuaApprove asks once before a mutating command. The preview names the target
// from metadata only and never carries screen content; only allow-once permits
// the command, so there is no session-wide grant.
func cuaApprove(ctx context.Context, appr cuaApprovals, class, preview string) error {
	req := &host.ApprovalRequest{
		Tool: computeruse.ToolName, Effect: host.EffectNonIdempotent,
		Class: "computer_use:" + class, Preview: "computer_use " + preview,
		Args: json.RawMessage(`{"kind":"` + class + `"}`),
	}
	if err := req.Validate(); err != nil {
		return fmt.Errorf("cua: %w", err)
	}
	answer, err := appr.Approve(ctx, req)
	if err != nil {
		return fmt.Errorf("cua: %w", err)
	}
	if answer != host.ApprovalAllowOnce {
		return fmt.Errorf("%w: %s", computeruse.ErrDenied, req.Class)
	}
	return nil
}

// cuaFlags carries the per-subcommand flag values; action.Kind is the
// subcommand name, which is also the closed action it drives.
type cuaFlags struct {
	action computeruse.Action
	out    string
	source string
	sha    string
	yes    bool
}

func cuaCommand(cmd string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("cua "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "config file (default: active profile)")
	var f cuaFlags
	cuaRegister(fs, cmd, &f)
	if err := fs.Parse(args); err != nil {
		return cuaExitUsage
	}
	if fs.NArg() != 0 {
		sessionsPrintf(stderr, "cua %s: unexpected argument %q\n", cmd, cleanArg(fs.Arg(0)))
		cuaUsage(stderr)
		return cuaExitUsage
	}
	appr := cuaApprovals{out: stderr, yes: f.yes}
	switch cmd {
	case "install":
		return cuaInstall(*configPath, &f, appr, stdout, stderr)
	case "doctor":
		return cuaDoctor(*configPath, stdout, stderr)
	}
	m, cfg, code := cuaOpen(cmd, *configPath, appr, stderr)
	if m == nil {
		return code
	}
	if err := cuaExecute(cmd, m, &f, appr, stdout, cfg); err != nil {
		return cuaFail(cmd, err, stderr)
	}
	return cuaExitOK
}

func cuaOpen(cmd, configPath string, appr cuaApprovals, stderr io.Writer) (*computeruse.Module, *config.Config, int) {
	loader, cfg, err := cuaLoad(configPath)
	if err != nil {
		sessionsPrintf(stderr, "cua %s: %s\n", cmd, cleanLine(err.Error()))
		return nil, nil, cuaExitUsage
	}
	if !cfg.Modules.ComputerUse.Enabled {
		sessionsPrintf(stderr, "cua %s: module_unavailable: computer_use is disabled (modules.computer_use.enabled)\n", cmd)
		return nil, nil, cuaExitUnavailable
	}
	m, code := cuaModule(loader, &cfg, appr, stderr)
	if m == nil {
		return nil, nil, code
	}
	return m, &cfg, cuaExitOK
}

// cuaModule builds the module on the CLI host seam; no backends start here.
func cuaModule(loader *config.Loader, cfg *config.Config, appr cuaApprovals, stderr io.Writer) (*computeruse.Module, int) {
	h := &cliHost{
		approve: appr,
		secrets: envSecrets{getenv: loader.Env},
		clock:   mcpWallClock{},
		errOut:  stderr,
	}
	caps := h.Capabilities()
	mc := computeruse.Config{
		Backend: cfg.Modules.ComputerUse.Backend, Endpoint: cfg.Modules.ComputerUse.Endpoint,
		Command: cfg.Modules.ComputerUse.Command, Env: cfg.Modules.ComputerUse.Env,
		Timeout:            time.Duration(cfg.Modules.ComputerUse.ActionTimeoutMS) * time.Millisecond,
		MaxScreenshotBytes: cfg.Modules.ComputerUse.MaxScreenshotBytes,
	}
	m, err := computeruse.New(&mc, computeruse.Deps{Spawn: h.Spawn, Approvals: caps.Approvals, Secrets: caps.Secrets})
	if err != nil {
		return nil, cuaFail("module", err, stderr)
	}
	return m, cuaExitOK
}

func cuaRegister(fs *flag.FlagSet, cmd string, f *cuaFlags) {
	f.action.Kind = computeruse.ActionKind(cmd)
	switch cmd {
	case "click":
		fs.IntVar(&f.action.X, "x", 0, "pointer x")
		fs.IntVar(&f.action.Y, "y", 0, "pointer y")
		fs.BoolVar(&f.yes, "yes", false, "approve this one click")
	case "type":
		fs.StringVar(&f.action.Text, "text", "", "text to type")
		fs.BoolVar(&f.yes, "yes", false, "approve this one type action")
	case "screenshot", "screen":
		fs.StringVar(&f.out, "out", "", "write the PNG here instead of the default attachment path")
		if cmd == "screen" {
			fs.BoolVar(&f.yes, "yes", false, "approve this one screen capture")
		}
	case "start", "stop":
		fs.BoolVar(&f.yes, "yes", false, "approve this one backend lifecycle action")
	case "install":
		fs.StringVar(&f.source, "source", "", "driver binary to stage under <data>/cua")
		fs.StringVar(&f.sha, "sha256", "", "expected SHA-256 of --source")
		fs.BoolVar(&f.yes, "yes", false, "approve this one install")
	}
}

func cuaExecute(cmd string, m *computeruse.Module, f *cuaFlags, appr cuaApprovals, stdout io.Writer, cfg *config.Config) error {
	ctx := context.Background()
	switch cmd {
	case "status":
		if err := m.Start(ctx); err != nil {
			return fmt.Errorf("cua: %w", err)
		}
		sessionsPrintf(stdout, "computer_use: backend=%s available=true\n", cfg.Modules.ComputerUse.Backend)
		return nil
	case "start":
		if err := cuaApprove(ctx, appr, cmd, cmd+" backend="+cuaBackend(cfg.Modules.ComputerUse.Backend)); err != nil {
			return err
		}
		if err := m.Start(ctx); err != nil {
			return fmt.Errorf("cua: %w", err)
		}
		sessionsPrintf(stdout, "computer_use: backend=%s started\n", cfg.Modules.ComputerUse.Backend)
		return nil
	case "stop":
		if err := cuaApprove(ctx, appr, cmd, cmd+" backend="+cuaBackend(cfg.Modules.ComputerUse.Backend)); err != nil {
			return err
		}
		if err := m.Stop(ctx); err != nil {
			return fmt.Errorf("cua: %w", err)
		}
		sessionsPrintf(stdout, "computer_use: backend=%s stopped\n", cfg.Modules.ComputerUse.Backend)
		return nil
	case "screenshot":
		png, err := m.Screenshot(ctx)
		if err != nil {
			return fmt.Errorf("cua: %w", err)
		}
		return cuaWriteShot(stdout, "screenshot", f.out, png)
	case "permissions":
		return cuaPermissions(ctx, m, stdout)
	case "screen":
		return cuaScreen(ctx, m, f.out, appr, stdout, cfg)
	default:
		if err := f.action.Validate(); err != nil {
			return fmt.Errorf("cua: %w", err)
		}
		res, err := m.Act(ctx, "cua-cli", &f.action)
		if err != nil {
			return fmt.Errorf("cua: %w", err)
		}
		sessionsPrintf(stdout, "%s\n", cleanLine(res.Content))
		return nil
	}
}

// cuaPermissions prints the OS permission view; it never grants or spawns a
// screen-touching command.
func cuaPermissions(ctx context.Context, m *computeruse.Module, stdout io.Writer) error {
	st, err := m.Permissions(ctx)
	if err != nil {
		return fmt.Errorf("cua: %w", err)
	}
	sessionsPrintf(stdout, "permissions: platform=%s tcc=%t ready=%t accessibility=%s screen_recording=%s read_only=true\n",
		st.Platform, st.Supported, st.Ready, cuaBool(st.Accessibility), cuaBool(st.ScreenRecording))
	if st.Detail != "" {
		sessionsPrintf(stdout, "permissions: %s\n", cleanLine(st.Detail))
	}
	return nil
}

// cuaScreen asks once, captures one bounded screenshot and writes the image
// attachment; the taint is printed so the caller never treats it as trusted.
func cuaScreen(ctx context.Context, m *computeruse.Module, out string, appr cuaApprovals, stdout io.Writer, cfg *config.Config) error {
	if err := cuaApprove(ctx, appr, "screen", "screen backend="+cuaBackend(cfg.Modules.ComputerUse.Backend)); err != nil {
		return err
	}
	res, err := m.Act(ctx, "cua-screen", &computeruse.Action{Kind: computeruse.ActionScreenshot})
	if err != nil {
		return fmt.Errorf("cua: %w", err)
	}
	if len(res.Attachments) != 1 {
		return errors.New("cua screen: backend returned no image attachment")
	}
	if out == "" {
		if out, err = cuaScreenPath(); err != nil {
			return err
		}
	}
	if err := cuaWriteShot(stdout, "screen", out, res.Attachments[0].Data); err != nil {
		return err
	}
	sessionsPrintf(stdout, "screen: untrusted source=%q (approval was per capture)\n", res.UntrustedSource)
	return nil
}

func cuaBackend(backend string) string {
	if backend == "" {
		return "process"
	}
	return backend
}

func cuaBool(v *bool) string {
	if v == nil {
		return "unknown"
	}
	return strconv.FormatBool(*v)
}

func cuaLoad(path string) (*config.Loader, config.Config, error) {
	loader, err := config.New(config.OS())
	if err != nil {
		return nil, config.Config{}, fmt.Errorf("config deps: %w", err)
	}
	cfg, err := loadRunConfig(loader, path)
	if err != nil {
		return nil, config.Config{}, err
	}
	return loader, cfg, nil
}

func cuaWriteShot(stdout io.Writer, label, path string, png []byte) error {
	if path == "" {
		return errors.New("cua screenshot: --out is required")
	}
	if len(png) == 0 {
		return errors.New("empty screenshot")
	}
	if err := os.WriteFile(path, png, 0o600); err != nil { // #nosec G703 -- explicit CLI output path
		return fmt.Errorf("write %s: %w", path, err)
	}
	sessionsPrintf(stdout, "%s: %d bytes -> %s\n", label, len(png), cleanLine(path))
	return nil
}

func cuaFail(cmd string, err error, stderr io.Writer) int {
	code := cuaExitFail
	label := "error"
	switch {
	case errors.Is(err, computeruse.ErrUnavailable):
		code, label = cuaExitUnavailable, "module_unavailable"
	case errors.Is(err, computeruse.ErrDenied):
		code, label = cuaExitDenied, "denied"
	case errors.Is(err, computeruse.ErrInvalid):
		code, label = cuaExitUsage, "invalid_args"
	case errors.Is(err, computeruse.ErrChecksum):
		code, label = cuaExitFail, "checksum"
	case errors.Is(err, computeruse.ErrTooLarge):
		code, label = cuaExitFail, "too_large"
	case errors.Is(err, computeruse.ErrTimeout), errors.Is(err, context.DeadlineExceeded):
		code, label = cuaExitFail, "timeout"
	}
	sessionsPrintf(stderr, "cua %s: %s: %s\n", cmd, label, cleanLine(err.Error()))
	return code
}

// cuaInstall stages the external driver under the data directory. It needs the
// same one-shot approval as every other mutating command and never touches the
// module or the screen.
func cuaInstall(configPath string, f *cuaFlags, appr cuaApprovals, stdout, stderr io.Writer) int {
	loader, _, err := cuaLoad(configPath)
	if err != nil {
		sessionsPrintf(stderr, "cua install: %s\n", cleanLine(err.Error()))
		return cuaExitUsage
	}
	paths, err := loader.Paths(activeProfile())
	if err != nil {
		sessionsPrintf(stderr, "cua install: %s\n", cleanLine(err.Error()))
		return cuaExitUsage
	}
	preview := "install source=recorded"
	if f.source != "" {
		preview = "install source=" + filepath.Base(f.source)
	}
	if aerr := cuaApprove(context.Background(), appr, "install", preview); aerr != nil {
		return cuaFail("install", aerr, stderr)
	}
	res, err := computeruse.Install(computeruse.InstallOptions{
		Dir: paths.DataDir, Source: f.source, SHA256: f.sha,
	})
	if err != nil {
		return cuaFail("install", err, stderr)
	}
	state := "already installed"
	if res.Installed {
		state = "installed"
	}
	if !res.Verified {
		state += " (unverified)"
	}
	sessionsPrintf(stdout, "install: %s %s sha256=%s\n", state, cleanLine(res.Path), res.SHA256)
	return cuaExitOK
}

// cuaDoctor diagnoses the configured backend without starting it or touching
// the screen: it only stats the binary and reads the OS permission state.
func cuaDoctor(configPath string, stdout, stderr io.Writer) int {
	loader, cfg, err := cuaLoad(configPath)
	if err != nil {
		sessionsPrintf(stderr, "cua doctor: %s\n", cleanLine(err.Error()))
		return cuaExitUsage
	}
	cu := cfg.Modules.ComputerUse
	backend := cuaBackend(cu.Backend)
	sessionsPrintf(stdout, "doctor: module=computer_use enabled=%t backend=%s\n", cu.Enabled, backend)
	if !cu.Enabled {
		sessionsPrintf(stdout, "doctor: check config fail (modules.computer_use.enabled=false)\n")
		return cuaExitUnavailable
	}
	sessionsPrintf(stdout, "doctor: check config ok\n")
	unavailable, degraded := cuaDoctorBackend(&cu, backend, stdout)
	if m, code := cuaModule(loader, &cfg, cuaApprovals{out: stderr}, stderr); m != nil {
		degraded = cuaDoctorPermissions(m, stdout) || degraded
	} else if code != cuaExitOK {
		degraded = true
	}
	sessionsPrintf(stdout, "doctor: check limits ok timeout_ms=%d max_screenshot_bytes=%d\n",
		cu.ActionTimeoutMS, cu.MaxScreenshotBytes)
	switch {
	case unavailable:
		return cuaExitUnavailable
	case degraded:
		return cuaExitFail
	}
	return cuaExitOK
}

// cuaDoctorBackend checks the selected transport without dialing it.
func cuaDoctorBackend(cu *config.ModulesComputerUse, backend string, stdout io.Writer) (unavailable, degraded bool) {
	if backend == "http" {
		if cu.Endpoint == "" {
			sessionsPrintf(stdout, "doctor: check endpoint fail (backend=http without endpoint)\n")
			return true, false
		}
		sessionsPrintf(stdout, "doctor: check endpoint ok %s\n", cleanLine(cu.Endpoint))
		return false, false
	}
	if len(cu.Command) == 0 {
		sessionsPrintf(stdout, "doctor: check driver fail (no command configured; run `damnbot cua install --source ... --yes`)\n")
		return true, false
	}
	info, err := os.Stat(cu.Command[0])
	switch {
	case err != nil:
		sessionsPrintf(stdout, "doctor: check driver fail (%s: %s)\n", cleanLine(cu.Command[0]), cleanLine(err.Error()))
		return true, false
	case !info.Mode().IsRegular():
		sessionsPrintf(stdout, "doctor: check driver fail (%s is not a regular file)\n", cleanLine(cu.Command[0]))
		return true, false
	case runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0:
		sessionsPrintf(stdout, "doctor: check driver degraded (%s is not executable)\n", cleanLine(cu.Command[0]))
		return false, true
	}
	sessionsPrintf(stdout, "doctor: check driver ok path=%s\n", cleanLine(cu.Command[0]))
	return false, false
}

// cuaDoctorPermissions reports the read-only permission probe.
func cuaDoctorPermissions(m *computeruse.Module, stdout io.Writer) (degraded bool) {
	st, err := m.Permissions(context.Background())
	if err != nil {
		sessionsPrintf(stdout, "doctor: check permissions fail (%s)\n", cleanLine(err.Error()))
		return true
	}
	verdict := "ok"
	if !st.Ready {
		verdict = "degraded"
	}
	sessionsPrintf(stdout, "doctor: check permissions %s platform=%s tcc=%t ready=%t\n",
		verdict, st.Platform, st.Supported, st.Ready)
	return !st.Ready
}

// cuaScreenPath is the default attachment destination `<data>/attachments`.
func cuaScreenPath() (string, error) {
	loader, err := config.New(config.OS())
	if err != nil {
		return "", fmt.Errorf("config deps: %w", err)
	}
	paths, err := loader.Paths(activeProfile())
	if err != nil {
		return "", fmt.Errorf("config paths: %w", err)
	}
	dir := filepath.Join(paths.DataDir, "attachments")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	return filepath.Join(dir, "cua-screen.png"), nil
}
