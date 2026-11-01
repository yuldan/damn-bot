//go:build windows

package backends

import (
	"context"

	"golang.org/x/crypto/ssh"
)

// agentAuth is unavailable: the Windows OpenSSH agent uses a named pipe, not
// SSH_AUTH_SOCK.
func agentAuth(context.Context, []string) ssh.AuthMethod { return nil }
