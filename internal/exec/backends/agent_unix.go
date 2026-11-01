//go:build unix

package backends

import (
	"context"
	"net"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// agentAuth uses SSH_AUTH_SOCK from the injected process environment (the
// variable only reaches the backend when exec.pass_ssh_agent allowed it, and
// internal/config is the only package that reads the OS environment).
func agentAuth(ctx context.Context, env []string) ssh.AuthMethod {
	sock := envValue(env, "SSH_AUTH_SOCK")
	if sock == "" {
		return nil
	}
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "unix", sock)
	if err != nil {
		return nil
	}
	return ssh.PublicKeysCallback(agent.NewClient(conn).Signers)
}

// envValue reads one KEY=VALUE from an injected environment slice.
func envValue(env []string, key string) string {
	for _, kv := range env {
		if name, value, ok := strings.Cut(kv, "="); ok && name == key {
			return value
		}
	}
	return ""
}
