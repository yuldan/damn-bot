package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/exec"
	"github.com/damnbot/damnbot/internal/exec/backends"
	"github.com/damnbot/damnbot/internal/secret"
)

// newExecBackend selects the shell backend of this run from config
// (EXEC-AND-CODING §A.7, ADR-275). Persistent sessions exist only for the
// local backend; a remote backend returns nil sessions, so the shell tool
// answers `session` with module_unavailable instead of emulating one.
// A backend that cannot be built here fails closed: there is no silent
// fallback to local.
// cloudConfig returns the configured provider block of the selected cloud
// backend.
func cloudConfig(cfg *config.Config) config.CloudExec {
	switch cfg.Exec.Backend {
	case "modal":
		return cfg.Exec.Backends.Modal
	case "daytona":
		return cfg.Exec.Backends.Daytona
	default:
		return cfg.Exec.Backends.Vercel
	}
}

func newExecBackend(ctx context.Context, cfg *config.Config, runner *exec.Runner, sessDir, workspace, configDir string, env []string) (exec.Backend, *exec.Sessions, error) {
	switch cfg.Exec.Backend {
	case "local", "":
		return exec.NewLocal(runner), runner.NewSessions(sessDir, env), nil
	case "docker":
		d := cfg.Exec.Backends.Docker
		b, err := backends.NewDocker(&backends.DockerConfig{
			Binary: d.Binary, Image: d.Image, User: d.User, Workspace: workspace,
			CPUMilli: d.CPUMilli, MemoryMiB: d.MemoryMiB, PIDs: d.PIDs,
			Network: d.Network, NetworkName: d.NetworkName, EgressAllow: d.EgressAllow, ProxyURL: d.ProxyURL,
			EnvAllow: cfg.Exec.EnvAllow, AllowUnlimited: d.AllowUnlimited,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("exec backend %s: %w", cfg.Exec.Backend, err)
		}
		return b, nil, nil
	case "singularity":
		s := cfg.Exec.Backends.Singularity
		b, err := backends.NewSingularity(&backends.SingularityConfig{
			Binary: s.Binary, Image: s.Image, Workspace: workspace, Network: s.Network,
			Fakeroot: s.Fakeroot, EnvAllow: cfg.Exec.EnvAllow, AllowUnlimited: s.AllowUnlimited,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("exec backend %s: %w", cfg.Exec.Backend, err)
		}
		return b, nil, nil
	case "ssh":
		s := cfg.Exec.Backends.SSH
		b, err := backends.NewSSH(ctx, &backends.SSHConfig{
			Host: s.Host, User: s.User, Port: s.Port, KeyFile: s.KeyFile,
			KnownHosts: s.KnownHosts, EnvAllow: cfg.Exec.EnvAllow,
			KeepAlive: time.Duration(s.KeepAliveS) * time.Second,
			IdleClose: time.Duration(s.IdleCloseS) * time.Second,
			Timeout:   time.Duration(s.TimeoutS) * time.Second,
			MaxConns:  s.MaxConns, Env: os.Environ(),
		})
		if err != nil {
			return nil, nil, fmt.Errorf("exec backend %s: %w", cfg.Exec.Backend, err)
		}
		return b, nil, nil
	case "modal", "daytona", "vercel_sandbox":
		cloud := cloudConfig(cfg)
		st, err := secret.New(secret.Options{ConfigDir: configDir})
		if err != nil {
			return nil, nil, fmt.Errorf("%w: %s: secret store: %w", exec.ErrUnavailable, cfg.Exec.Backend, err)
		}
		token, err := st.Get(ctx, cloud.TokenSecret)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: %s: token %q: %w", exec.ErrUnavailable, cfg.Exec.Backend, cloud.TokenSecret, err)
		}
		b, err := backends.NewCloud(&backends.CloudConfig{
			Provider: cfg.Exec.Backend, Endpoint: cloud.Endpoint, Token: token,
			Image: cloud.Image, Timeout: time.Duration(cloud.TimeoutS) * time.Second,
			ProjectID: cloud.ProjectID, TeamID: cloud.TeamID, EnvAllow: cfg.Exec.EnvAllow,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("exec backend %s: %w", cfg.Exec.Backend, err)
		}
		return b, nil, nil
	default:
		return nil, nil, fmt.Errorf("%w: %s: no backend implementation in this build", exec.ErrUnavailable, cfg.Exec.Backend)
	}
}
