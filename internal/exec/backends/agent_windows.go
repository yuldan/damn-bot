//go:build windows

package backends

import (
	"context"
	"net"

	"golang.org/x/crypto/ssh"
)

// agentAuth is unavailable: the Windows OpenSSH agent uses a named pipe, not
// SSH_AUTH_SOCK.
func agentAuth(context.Context, []string, func(context.Context, string, string) (net.Conn, error)) ssh.AuthMethod {
	return nil
}
