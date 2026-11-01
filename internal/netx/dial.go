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

// nonPublic lists special-purpose ranges outside the stdlib predicates
// (IANA registries; REVIEW-F1-SEC1 NETX-14).
func nonPublic() []netip.Prefix {
	return []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),     // "this network"
		netip.MustParsePrefix("100.64.0.0/10"), // CGNAT, RFC 6598
		netip.MustParsePrefix("192.0.0.0/24"),  // IETF protocol assignments
		netip.MustParsePrefix("198.18.0.0/15"), // benchmarking
		netip.MustParsePrefix("240.0.0.0/4"),   // reserved (incl. broadcast)
		netip.MustParsePrefix("fec0::/10"),     // deprecated site-local
	}
}

// embeddedV4 returns the IPv4 address carried by NAT64 (64:ff9b::/96),
// IPv4-compatible (::/96), and 6to4 (2002::/16) IPv6 addresses.
func embeddedV4(ip netip.Addr) (netip.Addr, bool) {
	if !ip.Is6() || ip.Is4In6() {
		return netip.Addr{}, false
	}
	b := ip.As16()
	switch {
	case netip.MustParsePrefix("64:ff9b::/96").Contains(ip), netip.MustParsePrefix("::/96").Contains(ip):
		return netip.AddrFrom4([4]byte(b[12:16])), true
	case netip.MustParsePrefix("2002::/16").Contains(ip):
		return netip.AddrFrom4([4]byte(b[2:6])), true
	}
	return netip.Addr{}, false
}

// publicIP reports whether ip is a globally routable unicast address: not
// loopback, private (RFC 1918, ULA), link-local (incl. cloud metadata
// 169.254.169.254), special-purpose (nonPublic), unspecified, or
// multicast; an IPv4 address embedded in IPv6 must itself be public.
func publicIP(ip netip.Addr) bool {
	if v4, ok := embeddedV4(ip); ok {
		return publicIP(v4)
	}
	if !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return false
	}
	for _, p := range nonPublic() {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// dialer returns the DialContext of a transport under rule. The check runs
// in ControlContext on the address actually being connected, after DNS
// resolution, so a rebinding name cannot slip past it. Every dial failure
// is a *ConnectError (nothing was sent).

// DialContext dials a non-HTTP protocol (SSH) under the Network policy.
func (n *Network) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return dialer(dialRule{offline: n.cfg.Offline, test: testBinary()})(ctx, network, addr)
}

func dialer(rule dialRule) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return dialerWith(rule, nil)
}

// dialerWith is dialer with an optional dialer supplied by a test (resolver,
// fallback delay, control). Production passes nil and keeps the documented
// net.Dialer. An override still goes through rule.check first, so the policy
// cannot be bypassed through the seam.
func dialerWith(rule dialRule, override *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	d := &net.Dialer{Timeout: dialTimeout, KeepAlive: dialKeepAlive}
	inner := (func(context.Context, string, string, syscall.RawConn) error)(nil)
	if override != nil {
		cp := *override
		d = &cp
		inner = override.ControlContext
	}
	policy := dialPolicy(rule)
	d.ControlContext = func(ctx context.Context, network, address string, raw syscall.RawConn) error {
		if err := policy(ctx, network, address, raw); err != nil {
			return err
		}
		if inner == nil {
			return nil
		}
		return inner(ctx, network, address, raw)
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := d.DialContext(ctx, network, addr)
		if err != nil {
			return nil, &ConnectError{Addr: addr, Err: err}
		}
		return c, nil
	}
}

// dialPolicy is the address policy hook shared by the production dialer and a
// test override.
func dialPolicy(rule dialRule) func(context.Context, string, string, syscall.RawConn) error {
	return func(_ context.Context, _, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil {
			return fmt.Errorf("dial address %q: %w", address, err)
		}
		return rule.check(ap.Addr())
	}
}

// testBinary reports whether the process is a `go test` binary (E-69).
func testBinary() bool { return testing.Testing() }
