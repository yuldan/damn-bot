package netx

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"syscall"
	"testing"
	"time"
)

// Dial parameters (ADR-100).
const (
	dialTimeout   = 10 * time.Second
	dialKeepAlive = 30 * time.Second
)

// dialRule is the address policy of one transport.
type dialRule struct {
	offline bool // network.offline: loopback only
	tool    bool // tool egress: public addresses only (anti SSRF)
	test    bool // running in a test binary: loopback only (E-69)
}

// check decides whether ip may be dialled under r. Loopback stays allowed
// for tool egress inside test binaries, where every dial is loopback-only
// anyway, so tool tests can use httptest servers.
func (r dialRule) check(ip netip.Addr) error {
	ip = ip.Unmap()
	switch {
	case r.test && !ip.IsLoopback():
		return refuse(ErrNonLoopbackInTest)
	case r.offline && !ip.IsLoopback():
		return refuse(ErrOffline)
	case r.tool && (!r.test || !ip.IsLoopback()) && !publicIP(ip):
		return refuse(ErrBlockedHost)
	}
	return nil
}

// cgnat is RFC 6598 shared address space (100.64.0.0/10).
func cgnat() netip.Prefix { return netip.PrefixFrom(netip.AddrFrom4([4]byte{100, 64, 0, 0}), 10) }

// publicIP reports whether ip is a globally routable unicast address:
// not loopback, private (RFC 1918, ULA), link-local (incl. cloud metadata
// 169.254.169.254), CGNAT, unspecified, multicast, or broadcast.
func publicIP(ip netip.Addr) bool {
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !cgnat().Contains(ip) &&
		ip != netip.AddrFrom4([4]byte{255, 255, 255, 255})
}

// dialer returns the DialContext of a transport under rule. The check runs
// in ControlContext on the address actually being connected, after DNS
// resolution, so a rebinding name cannot slip past it. Every dial failure
// is a *ConnectError (nothing was sent).
func dialer(rule dialRule) func(ctx context.Context, network, addr string) (net.Conn, error) {
	d := &net.Dialer{
		Timeout:   dialTimeout,
		KeepAlive: dialKeepAlive,
		ControlContext: func(_ context.Context, _, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil {
				return fmt.Errorf("dial address %q: %w", address, err)
			}
			return rule.check(ap.Addr())
		},
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := d.DialContext(ctx, network, addr)
		if err != nil {
			return nil, &ConnectError{Addr: addr, Err: err}
		}
		return c, nil
	}
}

// testBinary reports whether the process is a `go test` binary (E-69).
func testBinary() bool { return testing.Testing() }
