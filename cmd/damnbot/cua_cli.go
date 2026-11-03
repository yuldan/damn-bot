//go:build !minimal

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
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
	case "status", "start", "stop", "screenshot", "click", "type":
		return cuaCommand(args[0], args[1:], stdout, stderr)
	default:
		sessionsPrintf(stderr, "cua: unknown subcommand %q\n", cleanArg(args[0]))
		cuaUsage(stderr)
		return cuaExitUsage
	}
}

func cuaUsage(w io.Writer) {
	sessionsPrintf(w, "usage: damnbot cua status|start|stop|screenshot [flags]\n")
	sessionsPrintf(w, "       damnbot cua click --x N --y N --yes\n")
	sessionsPrintf(w, "       damnbot cua type --text STRING --yes\n")
	sessionsPrintf(w, "\ncommon flags: --config <path>. Input actions need --yes; screenshot is read-only and needs --out.\n")
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

func cuaCommand(cmd string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("cua "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "config file (default: active profile)")
	var (
		yes    bool
		out    string
		action computeruse.Action
	)
	cuaRegister(fs, cmd, &action, &yes, &out)
	if err := fs.Parse(args); err != nil {
		return cuaExitUsage
	}
	if fs.NArg() != 0 {
		sessionsPrintf(stderr, "cua %s: unexpected argument %q\n", cmd, cleanArg(fs.Arg(0)))
		cuaUsage(stderr)
		return cuaExitUsage
	}
	m, cfg, code := cuaOpen(cmd, *configPath, yes, stderr)
	if m == nil {
		return code
	}
	if err := cuaExecute(cmd, m, &action, out, stdout, cfg); err != nil {
		return cuaFail(cmd, err, stderr)
	}
	return cuaExitOK
}

func cuaOpen(cmd, configPath string, yes bool, stderr io.Writer) (*computeruse.Module, *config.Config, int) {
	loader, cfg, err := cuaLoad(configPath)
	if err != nil {
		sessionsPrintf(stderr, "cua %s: %s\n", cmd, cleanLine(err.Error()))
		return nil, nil, cuaExitUsage
	}
	if !cfg.Modules.ComputerUse.Enabled {
		sessionsPrintf(stderr, "cua %s: module_unavailable: computer_use is disabled (modules.computer_use.enabled)\n", cmd)
		return nil, nil, cuaExitUnavailable
	}
	h := &cliHost{
		approve: cuaApprovals{out: stderr, yes: yes},
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
		return nil, nil, cuaFail(cmd, err, stderr)
	}
	return m, &cfg, cuaExitOK
}

func cuaRegister(fs *flag.FlagSet, cmd string, action *computeruse.Action, yes *bool, out *string) {
	action.Kind = computeruse.ActionKind(cmd)
	switch cmd {
	case "click":
		fs.IntVar(&action.X, "x", 0, "pointer x")
		fs.IntVar(&action.Y, "y", 0, "pointer y")
		fs.BoolVar(yes, "yes", false, "approve this one click")
	case "type":
		fs.StringVar(&action.Text, "text", "", "text to type")
		fs.BoolVar(yes, "yes", false, "approve this one type action")
	case "screenshot":
		fs.StringVar(out, "out", "", "write the PNG here instead of printing metadata")
	}
}

func cuaExecute(cmd string, m *computeruse.Module, action *computeruse.Action, out string, stdout io.Writer, cfg *config.Config) error {
	ctx := context.Background()
	switch cmd {
	case "status":
		if err := m.Start(ctx); err != nil {
			return fmt.Errorf("cua: %w", err)
		}
		sessionsPrintf(stdout, "computer_use: backend=%s available=true\n", cfg.Modules.ComputerUse.Backend)
		return nil
	case "start":
		if err := m.Start(ctx); err != nil {
			return fmt.Errorf("cua: %w", err)
		}
		sessionsPrintf(stdout, "computer_use: backend=%s started\n", cfg.Modules.ComputerUse.Backend)
		return nil
	case "stop":
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
		return cuaWriteShot(stdout, out, png)
	default:
		if err := action.Validate(); err != nil {
			return fmt.Errorf("cua: %w", err)
		}
		res, err := m.Act(ctx, "cua-cli", action)
		if err != nil {
			return fmt.Errorf("cua: %w", err)
		}
		sessionsPrintf(stdout, "%s\n", cleanLine(res.Content))
		return nil
	}
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

func cuaWriteShot(stdout io.Writer, path string, png []byte) error {
	if path == "" {
		return errors.New("cua screenshot: --out is required")
	}
	if len(png) == 0 {
		return errors.New("empty screenshot")
	}
	if err := os.WriteFile(path, png, 0o600); err != nil { // #nosec G703 -- explicit CLI output path
		return fmt.Errorf("write %s: %w", path, err)
	}
	sessionsPrintf(stdout, "screenshot: %d bytes -> %s\n", len(png), cleanLine(path))
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
	case errors.Is(err, computeruse.ErrTooLarge):
		code, label = cuaExitFail, "too_large"
	case errors.Is(err, computeruse.ErrTimeout), errors.Is(err, context.DeadlineExceeded):
		code, label = cuaExitFail, "timeout"
	}
	sessionsPrintf(stderr, "cua %s: %s: %s\n", cmd, label, cleanLine(err.Error()))
	return code
}
