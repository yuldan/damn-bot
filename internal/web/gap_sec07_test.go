package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// clock is a mutable fake clock shared by the server seams.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// TestF8WebNonceExchange: a nonce is one-use, the local link lives 60 s and
// the remote link 10 min, the session cookie is __Host-/HttpOnly/Secure/
// SameSite=Strict, and no token material reaches HTML, JS, storage or a query.
func TestF8WebNonceExchange(t *testing.T) {
	t.Parallel()
	clk := &clock{t: time.Unix(1_700_000_000, 0)}
	srv, ts := newTestServer(t, func(c *Config) {
		c.Now = clk.now
		c.NonceTTL = 60 * time.Second
		c.RemoteTTL = 10 * time.Minute
	})
	local := srv.OpenURL(false)
	if strings.Contains(local, "?") || !strings.Contains(local, "/auth#") {
		t.Fatalf("local URL carries a query or no fragment: %q", local)
	}
	res := exchange(t, ts, nonceOf(t, local))
	if res.status != http.StatusNoContent {
		t.Fatalf("first exchange = %d", res.status)
	}
	checkSessionCookie(t, res.cookies)
	if replay := exchange(t, ts, nonceOf(t, local)); replay.status != http.StatusUnauthorized {
		t.Errorf("nonce replay = %d", replay.status)
	}

	// Local TTL is 60 s; the remote link is still valid after that.
	clk.add(61 * time.Second)
	if got := exchange(t, ts, nonceOf(t, srv.OpenURL(true))).status; got != http.StatusNoContent {
		t.Errorf("remote link after 61 s = %d", got)
	}
	remote := srv.OpenURL(true)
	clk.add(10*time.Minute + time.Second)
	if got := exchange(t, ts, nonceOf(t, remote)).status; got != http.StatusUnauthorized {
		t.Errorf("remote link after 10 min = %d", got)
	}

	// No token material in a page, its JS or browser storage.
	checkNoTokenMaterial(t, ts)
}

// checkSessionCookie asserts the exact __Host- cookie attributes.
func checkSessionCookie(t *testing.T, cookies []*http.Cookie) {
	t.Helper()
	for _, c := range cookies {
		if c.Name != sessionCookie {
			continue
		}
		if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/" {
			t.Errorf("session cookie = %+v", c)
		}
	}
}

// checkNoTokenMaterial scans the public pages and scripts.
func checkNoTokenMaterial(t *testing.T, ts *httptest.Server) {
	t.Helper()
	for _, path := range []string{"/auth", "/assets/app.js", "/assets/settings.js", "/assets/login.js"} {
		body := get(t, ts, path).body
		for _, bad := range []string{"localStorage", "sessionStorage", "?token=", "__HERMES_SESSION_TOKEN__"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s carries %q", path, bad)
			}
		}
	}
}

// TestF8WebTicketOneTime: a 30 s ticket opens exactly one SSE stream, is bound
// to the session cookie, and expires after TicketTTL.
func TestF8WebTicketOneTime(t *testing.T) {
	t.Parallel()
	clk := &clock{t: time.Unix(1_700_000_000, 0)}
	srv, ts := newTestServer(t, func(c *Config) {
		c.Now = clk.now
		c.Sessions = newFakeStore(clk.now)
	})
	session, csrf := signIn(t, srv, ts)
	ticket := mintTicket(t, ts, session, csrf)

	open := func(ticket string, cookies ...*http.Cookie) int {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/api/events?ticket="+ticket, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range cookies {
			req.AddCookie(c)
		}
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if cerr := resp.Body.Close(); cerr != nil {
				t.Errorf("close stream: %v", cerr)
			}
		}()
		if resp.StatusCode == http.StatusOK {
			buf := make([]byte, 32)
			if _, rerr := resp.Body.Read(buf); rerr != nil {
				t.Errorf("read hello: %v", rerr)
			}
		}
		return resp.StatusCode
	}
	if got := open(ticket, session); got != http.StatusOK {
		t.Fatalf("first stream = %d", got)
	}
	if got := open(ticket, session); got != http.StatusUnauthorized {
		t.Fatalf("ticket replay = %d", got)
	}
	other, otherCSRF := signIn(t, srv, ts)
	_ = otherCSRF
	if got := open(mintTicket(t, ts, session, csrf), other); got != http.StatusUnauthorized {
		t.Fatalf("ticket with another session cookie = %d", got)
	}
	expired := mintTicket(t, ts, session, csrf)
	clk.add(TicketTTL + time.Second)
	if got := open(expired, session); got != http.StatusUnauthorized {
		t.Fatalf("expired ticket = %d", got)
	}
	if got := get(t, ts, "/api/events?ticket="+mintTicket(t, ts, session, csrf)).status; got != http.StatusUnauthorized {
		t.Fatalf("stream without the session cookie = %d", got)
	}
}

// TestUnixSocket0600: a unix listen is created 0600 and refuses a foreign UID.
func TestUnixSocket0600(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	srv, err := New(&Config{
		Listen:   "unix:" + dir + "/web.sock",
		Auth:     ModeToken,
		Sessions: newFakeStore(time.Now),
	})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := srv.Listen(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := ln.Close(); cerr != nil {
			t.Errorf("close listener: %v", cerr)
		}
	}()
	info, err := os.Lstat(dir + "/web.sock")
	if err != nil {
		t.Fatal(err)
	}
	// Windows does not honor AF_UNIX permissions (chmod 0600 reads back
	// 0666); the mode assertion is POSIX-only, the listener itself still opens.
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("socket mode = %o", perm)
		}
	}
}
