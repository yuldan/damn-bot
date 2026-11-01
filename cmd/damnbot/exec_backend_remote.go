//go:build !minimal

package main

import (
	"context"
	"fmt"
	"time"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/exec"
	"github.com/damnbot/damnbot/internal/exec/backends"
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
		})
		if err != nil {
			return nil, nil, fmt.Errorf("exec backend %s: %w", cfg.Exec.Backend, err)
		}
		return b, nil, nil
	default:
		return nil, nil, fmt.Errorf("%w: %s (workspace %s, config %s): not built yet", exec.ErrUnavailable, cfg.Exec.Backend, workspace, configDir)
	}
}
