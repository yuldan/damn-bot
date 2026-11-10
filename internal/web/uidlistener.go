package web

import (
	"fmt"
	"net"
)

// uidListener refuses a unix peer with a different UID before the HTTP
// handler runs (GAP-SEC-07). TCP listeners are unaffected because this type
// is only wrapped around a unix socket.
type uidListener struct{ net.Listener }

func (l uidListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, fmt.Errorf("web: accept unix peer: %w", err)
		}
		if sameUID(c) {
			return c, nil
		}
		if cerr := c.Close(); cerr != nil {
			return nil, fmt.Errorf("web: close rejected peer: %w", cerr)
		}
	}
}
