//go:build !minimal

package main

import (
	"context"
	"fmt"
	"time"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/exec"
	"github.com/damnbot/damnbot/internal/exec/backends"
	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/secret"
)

// newRemoteExecBackend builds a non-local backend; every path fails closed
// when its prerequisite is missing.
func newRemoteExecBackend(ctx context.Context, cfg *config.Config, workspace, configDir string, env []string) (exec.Backend, *exec.Sessions, error) {
	switch cfg.Exec.Backend {
	case "docker":
		d := cfg.Exec.Backends.Docker
		b, err := backends.NewDocker(&backends.DockerConfig{
			Binary: d.Binary, Image: d.Image, User: d.User, Workspace: workspace,
			CPUMilli: d.CPUMilli, MemoryMiB: d.MemoryMiB, PIDs: d.PIDs,
			Network: d.Network, NetworkName: d.NetworkName, EgressAllow: d.EgressAllow,
			ProxyURL: d.ProxyURL, EnvAllow: cfg.Exec.EnvAllow, AllowUnlimited: d.AllowUnlimited,
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
			MaxConns:  s.MaxConns, Env: env,
			Dial: netx.New(netx.Config{Offline: cfg.Network.Offline}).DialContext,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("exec backend %s: %w", cfg.Exec.Backend, err)
		}
		return b, nil, nil
	case "singularity":
		s := cfg.Exec.Backends.Singularity
		b, err := backends.NewSingularity(&backends.SingularityConfig{
			Binary: s.Binary, Image: s.Image, Workspace: workspace,
			CPUMilli: s.CPUMilli, MemoryMiB: s.MemoryMiB, PIDs: s.PIDs,
			Network: s.Network, NetworkName: s.NetworkName, EgressAllow: s.EgressAllow,
			ProxyURL: s.ProxyURL, EnvAllow: cfg.Exec.EnvAllow, AllowUnlimited: s.AllowUnlimited,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("exec backend %s: %w", cfg.Exec.Backend, err)
		}
		return b, nil, nil
	case "modal", "daytona", "vercel_sandbox":
		b, err := newSandboxBackend(cfg, configDir, cfg.Exec.Backend)
		if err != nil {
			return nil, nil, fmt.Errorf("exec backend %s: %w", cfg.Exec.Backend, err)
		}
		return b, nil, nil
	default:
		return nil, nil, fmt.Errorf("%w: %s (workspace %s, config %s): not built yet", exec.ErrUnavailable, cfg.Exec.Backend, workspace, configDir)
	}
}

func newSandboxBackend(cfg *config.Config, configDir, id string) (exec.Backend, error) {
	s := map[string]config.SandboxExec{"modal": cfg.Exec.Backends.Modal, "daytona": cfg.Exec.Backends.Daytona}[id]
	if id == "vercel_sandbox" {
		s = cfg.Exec.Backends.VercelSandbox
	}
	st, err := secret.New(secret.Options{ConfigDir: configDir})
	if err != nil {
		return nil, fmt.Errorf("%w: secret store: %w", exec.ErrUnavailable, err)
	}
	net := netx.New(netx.Config{Offline: cfg.Network.Offline, NoBackground: !cfg.Network.Background})
	b, err := backends.NewSandbox(&backends.SandboxConfig{
		Provider: id, Endpoint: s.Endpoint,
		Token:   func(ctx context.Context) (string, error) { return st.Get(ctx, s.Token) },
		Runtime: s.Runtime, ProjectID: s.ProjectID,
		Timeout:  time.Duration(s.TimeoutS) * time.Second,
		EnvAllow: cfg.Exec.EnvAllow,
		Client:   net.Client("exec-"+id, netx.Exec),
	})
	if err != nil {
		return nil, fmt.Errorf("sandbox %s: %w", id, err)
	}
	return b, nil
}
