// Package dialer is a netx subpackage, also allowed.
package dialer

import "net"

func Dial(addr string) (net.Conn, error) { return net.Dial("tcp", addr) }

var _ = net.Dialer{}
