package netx

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// TestDualStackFallback (EC-CFG-004): the transport dialer races the address
// families a name resolves to, so a blackholed first address does not stall
// the connection (EDGE-CASES-hermes.md:247: IPv6 hang memperlambat koneksi,
// happy eyeballs tidak dipakai). Production uses net.Dialer's normal path;
// the test supplies a resolver and a blocking address check through the
// unexported seam, making the race deterministic without network access.
func TestDualStackFallback(t *testing.T) {
	t.Parallel()
	g := phase.Current(t)
	if !g.Item(t, "EC-CFG-004", "F3", "covered") {
		return
	}
	t.Run("EC-CFG-004", func(t *testing.T) {
		t.Parallel()
		t.Run("first address blackholed", func(t *testing.T) {
			t.Parallel()
			dualStackSucceeds(t)
		})
		t.Run("both addresses dead", func(t *testing.T) {
			t.Parallel()
			dualStackFails(t)
		})
	})
}

// dualStackHarness is one IPv4 listener plus the two loopback addresses the
// fake resolver returns: one family is blackholed by the address check.
type dualStackHarness struct {
	target string
	good   string
	bad    string
	seen   atomic.Int32
	ready  chan struct{}
	once   sync.Once
	// blockAll makes the negative control blackhole every address.
	blockAll bool
}

func newDualStackHarness(t *testing.T) *dualStackHarness {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() {
		if cerr := ln.Close(); cerr != nil {
			t.Errorf("close listener: %v", cerr)
		}
	})
	go func() {
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			if cerr := c.Close(); cerr != nil {
				return
			}
		}
	}()
	host, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split listener: %v", err)
	}
	return &dualStackHarness{
		target: net.JoinHostPort("dual.test", port),
		good:   host,
		bad:    "::1",
		ready:  make(chan struct{}),
	}
}

// control blocks the bad family until the dial context ends, and holds the
// good family until a second address was attempted. A dialer that tries only
// one family can therefore never finish, so a success proves the race.
func (h *dualStackHarness) control(ctx context.Context, _, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("split dial address: %w", err)
	}
	if h.seen.Add(1) >= 2 {
		h.once.Do(func() { close(h.ready) })
	}
	if h.blockAll || host == h.bad {
		<-ctx.Done()
		return fmt.Errorf("blackholed address %s: %w", address, ctx.Err())
	}
	select {
	case <-h.ready:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("dial cancelled: %w", ctx.Err())
	}
}

func (h *dualStackHarness) dialer(t *testing.T) func(context.Context, string, string) (net.Conn, error) {
	t.Helper()
	return dialerWith(dialRule{test: true}, &net.Dialer{
		Timeout:       3 * time.Second,
		FallbackDelay: 50 * time.Millisecond,
		Resolver:      fakeResolver(t, netip.MustParseAddr(h.good), netip.MustParseAddr(h.bad)),
		ControlContext: func(ctx context.Context, network, address string, raw syscall.RawConn) error {
			return h.control(ctx, network, address, raw)
		},
	})
}

func dualStackSucceeds(t *testing.T) {
	t.Helper()
	h := newDualStackHarness(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	start := time.Now()
	conn, err := h.dialer(t)(ctx, "tcp", h.target)
	if err != nil {
		t.Fatalf("dial %s: %v (attempts=%d)", h.target, err, h.seen.Load())
	}
	defer func() {
		if cerr := conn.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	}()
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("dual-stack dial took %v, want a bounded fallback", elapsed)
	}
	if got := conn.RemoteAddr().String(); !strings.HasPrefix(got, h.good+":") {
		t.Fatalf("connected to %s, want %s", got, h.good)
	}
	if got := h.seen.Load(); got != 2 {
		t.Fatalf("attempted %d addresses, want 2", got)
	}
}

func dualStackFails(t *testing.T) {
	t.Helper()
	h := newDualStackHarness(t)
	h.blockAll = true
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := h.dialer(t)(ctx, "tcp", h.target)
	if err == nil {
		t.Fatal("dial succeeded with every address blackholed")
	}
	var ce *ConnectError
	if !errors.As(err, &ce) || ce.Addr != h.target {
		t.Fatalf("error = %T %v, want *ConnectError for %s", err, err, h.target)
	}
	if !strings.Contains(err.Error(), "connect "+h.target) {
		t.Fatalf("error %q does not name the target", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("blackholed dial took %v, want the context bound", elapsed)
	}
	if got := h.seen.Load(); got != 2 {
		t.Fatalf("attempted %d addresses, want 2", got)
	}
}

// packetConn makes the in-memory pipe take the resolver's packet path, so no
// DNS-over-TCP length prefix is involved.
type packetConn struct{ net.Conn }

func (p packetConn) ReadFrom(b []byte) (int, net.Addr, error) {
	n, err := p.Read(b)
	if err != nil {
		return n, p.RemoteAddr(), fmt.Errorf("read: %w", err)
	}
	return n, p.RemoteAddr(), nil
}

func (p packetConn) WriteTo(b []byte, _ net.Addr) (int, error) {
	n, err := p.Write(b)
	if err != nil {
		return n, fmt.Errorf("write: %w", err)
	}
	return n, nil
}

// fakeResolver answers A and AAAA queries from v4/v6 over an in-memory DNS
// connection: no dependency and no real DNS.
func fakeResolver(t *testing.T, v4, v6 netip.Addr) *net.Resolver {
	t.Helper()
	return &net.Resolver{
		PreferGo: true,
		Dial: func(context.Context, string, string) (net.Conn, error) {
			client, server := net.Pipe()
			go func() {
				if derr := server.SetDeadline(time.Now().Add(2 * time.Second)); derr != nil {
					return
				}
				serveDNS(server, v4, v6)
				if cerr := server.Close(); cerr != nil {
					return
				}
			}()
			return packetConn{Conn: client}, nil
		},
	}
}

// serveDNS answers one DNS query on conn.
func serveDNS(conn net.Conn, v4, v6 netip.Addr) {
	query := make([]byte, 512)
	n, err := conn.Read(query)
	if err != nil {
		return
	}
	resp := dnsResponse(query[:n], v4, v6)
	if _, err := conn.Write(resp); err != nil {
		return
	}
}

// dnsResponse echoes the question and adds one A or AAAA record; other query
// types get NODATA.
func dnsResponse(query []byte, v4, v6 netip.Addr) []byte {
	end, ok := questionEnd(query)
	if !ok {
		return nil
	}
	qtype := binary.BigEndian.Uint16(query[end-4:])
	var record []byte
	switch {
	case qtype == 1 && v4.Is4():
		a := v4.As4()
		record = append([]byte{0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4}, a[:]...)
	case qtype == 28 && v6.Is6():
		a := v6.As16()
		record = append([]byte{0xc0, 0x0c, 0, 28, 0, 1, 0, 0, 0, 60, 0, 16}, a[:]...)
	}
	resp := make([]byte, 12, 12+end-12+len(record))
	resp[0], resp[1] = query[0], query[1]
	resp[2], resp[3] = 0x81, 0x80
	resp[5] = 1 // QDCOUNT
	if record != nil {
		resp[7] = 1 // ANCOUNT
	}
	resp = append(resp, query[12:end]...)
	return append(resp, record...)
}

// questionEnd returns the offset after the question's qtype and qclass.
func questionEnd(query []byte) (int, bool) {
	if len(query) < 12 {
		return 0, false
	}
	off := 12
	for off < len(query) {
		size := int(query[off])
		if size == 0 {
			off++
			break
		}
		if size&0xc0 != 0 {
			return 0, false
		}
		off += size + 1
	}
	if off == 12 || off+4 > len(query) {
		return 0, false
	}
	return off + 4, true
}
