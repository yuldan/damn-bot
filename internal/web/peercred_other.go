//go:build !linux

package web

import "net"

// sameUID is a no-op on platforms without SO_PEERCRED; the socket mode 0600
// remains the local access boundary there.
func sameUID(net.Conn) bool { return true }
