package netx

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/netx/retry"
	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// newNet returns a Network closed (idle connections) at cleanup.
func newNet(t *testing.T, cfg Config) *Network {
	t.Helper()
	n := New(cfg)
	t.Cleanup(n.CloseIdleConnections)
	return n
}

// server starts an httptest server counting new TCP connections.
func server(t *testing.T, h http.HandlerFunc) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var conns atomic.Int32
	srv := httptest.NewUnstartedServer(h)
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)
	return srv, &conns
}

// closeBody closes a response that a refused request should not have.
func closeBody(t *testing.T, resp *http.Response) {
	t.Helper()
	if resp != nil {
		t.Errorf("unexpected response %s", resp.Status)
		if err := resp.Body.Close(); err != nil {
			t.Error(err)
		}
	}
}

// write is a handler write; a failed write means the client went away.
func write(w io.Writer, s string) bool {
	_, err := io.WriteString(w, s)
	return err == nil
}

// hangUp hijacks the connection and closes it, with RST if rst.
func hangUp(t *testing.T, w http.ResponseWriter, rst bool) {
	t.Helper()
	conn, _, err := http.NewResponseController(w).Hijack()
	if err != nil {
		t.Error(err)
		return
	}
	if tc, ok := conn.(*net.TCPConn); ok && rst {
		if err := tc.SetLinger(0); err != nil {
			t.Error(err)
		}
	}
	if err := conn.Close(); err != nil {
		t.Error(err)
	}
}

func get(t *testing.T, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

// TestNoNetwork (E-69): a test binary never dials a non-loopback address;
// the refusal happens before connect, so no packet leaves the host.
func TestNoNetwork(t *testing.T) {
	t.Parallel()
	if !testBinary() {
		t.Fatal("testing.Testing() = false in a test binary")
	}
	n := newNet(t, Config{})
	for _, p := range []Purpose{Inference, Tool, Background} {
		for _, u := range []string{"http://192.0.2.1/", "https://[2001:db8::1]:8443/x?key=secret"} {
			resp, err := n.Client("t", p).Do(get(t, u))
			closeBody(t, resp)
			_, isConnect := errors.AsType[*ConnectError](err)
			if !errors.Is(err, ErrNonLoopbackInTest) || !isConnect || retry.ClassifyErr(err) != retry.OfflineMode {
				t.Errorf("purpose %d %s: %v", p, u, err)
			}
			if err != nil && strings.Contains(err.Error(), "secret") {
				t.Errorf("error leaks the URL query: %v", err)
			}
		}
	}
}

func TestDialRule(t *testing.T) {
	t.Parallel()
	blocked := []string{
		"127.0.0.1", "::1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "100.64.0.1", "100.127.255.254",
		"169.254.169.254", "fe80::1", "fc00::1", "fd12::1", "0.0.0.0", "::", "224.0.0.1", "ff02::1",
		"255.255.255.255", "::ffff:10.0.0.1", "::ffff:127.0.0.1",
		// NETX-14: embedded IPv4 and IANA special-purpose ranges.
		"64:ff9b::a9fe:a9fe", "::7f00:1", "2002:7f00:1::", "2002:a9fe:a9fe::1", "fec0::1", "0.1.2.3",
		"198.18.0.1", "198.19.255.1", "240.0.0.1", "192.0.0.8",
	}
	public := []string{"8.8.8.8", "1.1.1.1", "100.128.0.1", "2001:4860:4860::8888", "::ffff:8.8.8.8", "64:ff9b::808:808", "2002:808:808::1", "198.20.0.1"}
	cases := []struct {
		rule  dialRule
		ip    string
		wantE error
	}{
		{dialRule{}, "8.8.8.8", nil},
		{dialRule{}, "10.0.0.1", nil},
		{dialRule{offline: true}, "127.0.0.1", nil},
		{dialRule{offline: true}, "::1", nil},
		{dialRule{offline: true}, "8.8.8.8", ErrOffline},
		{dialRule{offline: true}, "192.168.0.1", ErrOffline},
		{dialRule{test: true}, "8.8.8.8", ErrNonLoopbackInTest},
		{dialRule{test: true, tool: true}, "127.0.0.1", nil},
		{dialRule{test: true, tool: true}, "10.0.0.1", ErrNonLoopbackInTest},
		{dialRule{offline: true, tool: true}, "127.0.0.1", ErrBlockedHost},
	}
	for _, ip := range blocked {
		cases = append(cases, struct {
			rule  dialRule
			ip    string
			wantE error
		}{dialRule{tool: true}, ip, ErrBlockedHost})
	}
	for _, ip := range public {
		cases = append(cases, struct {
			rule  dialRule
			ip    string
			wantE error
		}{dialRule{tool: true}, ip, nil})
	}
	for _, c := range cases {
		err := c.rule.check(netip.MustParseAddr(c.ip))
		if !errors.Is(err, c.wantE) || (c.wantE == nil) != (err == nil) {
			t.Errorf("%+v %s: %v, want %v", c.rule, c.ip, err, c.wantE)
		}
	}
	if c := retry.ClassifyErr(refuse(ErrBlockedHost)); c != retry.BadRequest {
		t.Errorf("blocked_host class %s", c)
	}
}

// TestConnReuse (SPEC §17.2): 20 calls through clients of one Network use
// one TCP connection; loopback tool egress works inside test binaries.
func TestConnReuse(t *testing.T) {
	t.Parallel()
	srv, conns := server(t, func(w http.ResponseWriter, _ *http.Request) { write(w, "ok") })
	n := newNet(t, Config{Offline: true})
	for i := range 20 {
		resp, err := n.Client(fmt.Sprint("c", i%3), Inference).DoOK(get(t, srv.URL))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(io.Discard, resp.Body); err != nil {
			t.Fatal(err)
		}
		if err := resp.Body.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if got := conns.Load(); got != 1 {
		t.Errorf("%d TCP connections for 20 calls, want 1", got)
	}
	resp, err := n.Client("fetch", Tool).Do(get(t, srv.URL))
	if err != nil {
		t.Fatalf("tool egress to loopback in test binary: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestEgressDisabled (GAP-OPS-02): background egress is refused without a
// request when network.background = false; inference is unaffected.
func TestEgressDisabled(t *testing.T) {
	t.Parallel()
	srv, conns := server(t, func(http.ResponseWriter, *http.Request) {})
	n := newNet(t, Config{NoBackground: true})
	resp, err := n.Client("update", Background).Do(get(t, srv.URL))
	closeBody(t, resp)
	if !errors.Is(err, ErrEgressDisabled) || retry.ClassifyErr(err) != retry.OfflineMode || conns.Load() != 0 {
		t.Fatalf("background: %v, %d connections", err, conns.Load())
	}
	resp, err = n.Client("provider", Inference).DoOK(get(t, srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStatusError(t *testing.T) {
	t.Parallel()
	date := time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)
	cases := []struct {
		status    int
		header    string
		wantClass retry.Class
		minRA     time.Duration
	}{
		{http.StatusTooManyRequests, "7", retry.RateLimited, 7 * time.Second},
		{http.StatusTooManyRequests, date, retry.RateLimited, 59 * time.Minute},
		{http.StatusTooManyRequests, "soon", retry.RateLimited, 0},
		{http.StatusServiceUnavailable, "", retry.Overloaded, 0},
		{http.StatusUnauthorized, "", retry.Auth, 0},
	}
	for _, c := range cases {
		srv, _ := server(t, func(w http.ResponseWriter, _ *http.Request) {
			if c.header != "" {
				w.Header().Set("Retry-After", c.header)
			}
			w.WriteHeader(c.status)
			write(w, `{"error":{"code":"invalid_api_key","message":"please log in again"}}`)
		})
		resp, err := newNet(t, Config{}).Client("p", Inference).DoOK(get(t, srv.URL))
		closeBody(t, resp)
		var se *StatusError
		if !errors.As(err, &se) || se.StatusCode != c.status || !strings.Contains(string(se.Body), "invalid_api_key") {
			t.Fatalf("%d: %v", c.status, err)
		}
		if retry.ClassifyErr(err) != c.wantClass || se.RetryAfter() < c.minRA || se.RetryAfter() > c.minRA+time.Hour {
			t.Errorf("%d %q: class %s retry-after %v", c.status, c.header, retry.ClassifyErr(err), se.RetryAfter())
		}
	}
}

// sseServer streams events: each element is written and flushed, then the
// handler waits for the client to go away (or block forever if hold).
func sseServer(t *testing.T, chunks []string, gap time.Duration, hold bool) *httptest.Server {
	t.Helper()
	srv, _ := server(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, ok := w.(http.Flusher)
		if !ok {
			t.Error("no http.Flusher")
			return
		}
		for _, c := range chunks {
			if !write(w, c) {
				return
			}
			fl.Flush()
			select {
			case <-time.After(gap):
			case <-r.Context().Done():
				return
			}
		}
		if hold {
			<-r.Context().Done()
		}
	})
	return srv
}

func openStream(t *testing.T, url string, ttfb, idle time.Duration) *Stream {
	t.Helper()
	c := newNet(t, Config{}).Client("p", Inference)
	c.idle = idle
	s, err := c.Stream(t.Context(), get(t, url), ttfb)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}

func TestStream(t *testing.T) {
	t.Parallel()
	srv := sseServer(t, []string{"retry: 5\n", "data: a\n\n", ": ping\n\n", "event: e\ndata: b\n\n"}, 0, false)
	s := openStream(t, srv.URL, time.Minute, time.Minute)
	var got []string
	for {
		ev, err := s.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, string(ev.Type)+"/"+string(ev.Data))
	}
	if strings.Join(got, ",") != "/a,e/b" {
		t.Errorf("events %v", got)
	}
	if d, ok := s.Retry(); !ok || d != 5*time.Millisecond {
		t.Errorf("retry %v %v", d, ok)
	}
	srv404, _ := server(t, http.NotFound)
	_, err := newNet(t, Config{}).Client("p", Inference).Stream(t.Context(), get(t, srv404.URL), time.Minute)
	if retry.ClassifyErr(err) != retry.BadRequest {
		t.Errorf("404 stream: %v", err)
	}
}

func TestTTFB(t *testing.T) {
	t.Parallel()
	for in, want := range map[int]time.Duration{0: 25 * time.Second, 1: 30 * time.Second, 32 << 10: 30 * time.Second, 32<<10 + 1: 35 * time.Second, 1 << 30: 90 * time.Second} {
		if got := TTFB(in); got != want {
			t.Errorf("TTFB(%d) = %v, want %v", in, got, want)
		}
	}
	// Headers never arrive: the TTFB timer aborts Stream itself.
	stall, _ := server(t, func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	_, err := newNet(t, Config{}).Client("p", Inference).Stream(t.Context(), get(t, stall.URL), 50*time.Millisecond)
	if !errors.Is(err, ErrTTFB) || retry.ClassifyErr(err) != retry.Timeout {
		t.Errorf("stalled headers: %v (%s)", err, retry.ClassifyErr(err))
	}
	// Headers but no event: Next fails with ErrTTFB. The deadline leaves
	// slow runners time to deliver the headers (flushed at once) first.
	s := openStream(t, sseServer(t, []string{": hi\n\n"}, 0, true).URL, time.Second, time.Minute)
	if _, err := s.Next(); !errors.Is(err, ErrTTFB) || retry.ClassifyErr(err) != retry.Timeout {
		t.Errorf("no first event: %v", err)
	}
}

// TestEdgeCases (EDGE-CASES-hermes.md, ADR-077/080): entries owned by netx.
func TestEdgeCases(t *testing.T) {
	t.Parallel()
	g := phase.Current(t)
	cases := []struct {
		id string
		fn func(t *testing.T)
	}{
		{"EC-PROV-009", ecKeepaliveNoReset},
		{"EC-PROV-028", ecConfigNotShared},
		{"EC-PROV-030", ecResetVsRefused},
	}
	for _, c := range cases {
		if g.Item(t, c.id, "F1", "covered") {
			t.Run(c.id, c.fn)
		}
	}
}

// ecKeepaliveNoReset: keepalive comments every 10 ms do not keep a stream
// alive past the idle limit; only events do.
func ecKeepaliveNoReset(t *testing.T) {
	pings := make([]string, 200)
	for i := range pings {
		pings[i] = ": keepalive\n\n"
	}
	srv := sseServer(t, append([]string{"data: first\n\n"}, pings...), 10*time.Millisecond, true)
	s := openStream(t, srv.URL, time.Minute, 150*time.Millisecond)
	if ev, err := s.Next(); err != nil || string(ev.Data) != "first" {
		t.Fatalf("first event: %q %v", ev.Data, err)
	}
	start := time.Now()
	_, err := s.Next()
	if !errors.Is(err, ErrIdle) || retry.ClassifyErr(err) != retry.Timeout {
		t.Fatalf("zombie stream: %v", err)
	}
	if el := time.Since(start); el > 1500*time.Millisecond {
		t.Errorf("idle expiry after %v with keepalives, want ≈ 150 ms", el)
	}
}

// ecConfigNotShared: New copies Config; clients never mutate shared state;
// CloseIdleConnections does not leave a dead transport behind.
func ecConfigNotShared(t *testing.T) {
	srv, _ := server(t, func(http.ResponseWriter, *http.Request) {})
	cfg := Config{}
	n := newNet(t, cfg)
	cfg.NoBackground = true
	n.CloseIdleConnections()
	for _, p := range []Purpose{Background, Inference} {
		resp, err := n.Client("x", p).DoOK(get(t, srv.URL))
		if err != nil {
			t.Fatalf("purpose %d after config change + CloseIdleConnections: %v", p, err)
		}
		if err := resp.Body.Close(); err != nil {
			t.Fatal(err)
		}
	}
	other := newNet(t, cfg)
	resp, err := other.Client("x", Background).Do(get(t, srv.URL))
	closeBody(t, resp)
	if !errors.Is(err, ErrEgressDisabled) {
		t.Errorf("second network ignores its own config: %v", err)
	}
}

// ecResetVsRefused: connect refused and a connection lost after the request
// went out are distinct errors (both network, both retryable).
func ecResetVsRefused(t *testing.T) {
	var lc net.ListenConfig
	l, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if cerr := l.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	n := newNet(t, Config{})
	resp, err := n.Client("p", Inference).Do(get(t, "http://"+addr+"/"))
	closeBody(t, resp)
	_, isConnect := errors.AsType[*ConnectError](err)
	if !isConnect || errors.Is(err, ErrConnectionLost) || retry.ClassifyErr(err) != retry.Network {
		t.Errorf("refused: %v", err)
	}
	early, _ := server(t, func(w http.ResponseWriter, _ *http.Request) { hangUp(t, w, false) })
	resp, err = n.Client("p", Inference).Do(get(t, early.URL))
	closeBody(t, resp)
	if !errors.Is(err, ErrConnectionLost) || retry.ClassifyErr(err) != retry.Network {
		t.Errorf("closed before response: %v", err)
	}
	ecMidBodyReset(t, n)
}

// ecMidBodyReset: an RST while the body streams is ErrConnectionLost.
func ecMidBodyReset(t *testing.T, n *Network) {
	srv, _ := server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if !write(w, "data: partial\n\n") {
			return
		}
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Error(err)
			return
		}
		hangUp(t, w, true)
	})
	c := n.Client("p", Inference)
	s, err := c.Stream(t.Context(), get(t, srv.URL), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := s.Close(); cerr != nil {
			t.Error(cerr)
		}
	}()
	if _, err = s.Next(); err != nil {
		t.Fatalf("first event: %v", err)
	}
	_, err = s.Next()
	_, isConnect := errors.AsType[*ConnectError](err)
	if !errors.Is(err, ErrConnectionLost) || isConnect || retry.ClassifyErr(err) != retry.Network {
		t.Errorf("mid-body reset: %v", err)
	}
}
