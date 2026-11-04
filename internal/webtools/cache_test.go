//go:build !minimal

package webtools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/modules/host"
)

func TestCacheTTLAndLimits(t *testing.T) {
	t.Parallel()
	now := time.Unix(1000, 0)
	c := newCache(2, 40, func() time.Time { return now })
	var calls int
	load := func(key string, size int) (any, bool, error) {
		return c.load(key, time.Minute, func() (any, int, error) {
			calls++
			return key, size, nil
		})
	}
	if _, hit, err := load("a", 10); err != nil || hit {
		t.Errorf("first load hit=%v err=%v", hit, err)
	}
	if _, hit, err := load("a", 10); err != nil || !hit {
		t.Errorf("second load hit=%v err=%v", hit, err)
	}
	now = now.Add(2 * time.Minute)
	if _, hit, err := load("a", 10); err != nil || hit || calls != 2 {
		t.Errorf("expired load hit=%v calls=%d", hit, calls)
	}
	if _, _, err := load("b", 20); err != nil {
		t.Fatal(err)
	}
	if _, _, err := load("c", 20); err != nil {
		t.Fatal(err)
	}
	if c.len() != 2 || calls != 4 {
		t.Errorf("len=%d calls=%d", c.len(), calls)
	}
	if _, hit, err := load("d", 50); err != nil || hit || c.len() != 2 {
		t.Errorf("oversize entry stored: hit=%v len=%d", hit, c.len())
	}
}

func TestCacheSingleFlight(t *testing.T) {
	t.Parallel()
	c := newCache(10, 1<<20, nil)
	var calls atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 10 {
		wg.Go(func() {
			<-start
			_, _, err := c.load("same", time.Minute, func() (any, int, error) {
				calls.Add(1)
				<-time.After(10 * time.Millisecond)
				return "v", 1, nil
			})
			if err != nil {
				t.Error(err)
			}
		})
	}
	close(start)
	wg.Wait()
	if got := calls.Load(); got != 1 {
		t.Fatalf("backend calls = %d, want 1", got)
	}
}

func TestWebCacheIsolation(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if _, err := w.Write([]byte("<h1>Isi</h1>")); err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()
	m := startModule(t, webConfig())
	a := session(t, "tenant-a")
	b := session(t, "tenant-b")
	for range 30 {
		if _, err := m.Invoke(context.Background(), "web_fetch", json.RawMessage(`{"url":"`+srv.URL+`"}`), a); err != nil {
			t.Fatal(err)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("30 fetches -> %d backend calls, want 1", got)
	}
	if _, err := m.Invoke(context.Background(), "web_fetch", json.RawMessage(`{"url":"`+srv.URL+`"}`), b); err != nil {
		t.Fatal(err)
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("other tenant shared cache: %d calls", got)
	}

	webCacheProfileScope(t, srv)
}

// webCacheProfileScope pins GAP-COST-12: sibling subagents of one profile
// share a cache entry, profiles never do, and the 20/60-minute TTLs hold.
func webCacheProfileScope(t *testing.T, srv *httptest.Server) {
	// GAP-COST-12: three delegate siblings of one profile share one vendor
	// call, while two independent profiles never share a hit.
	var searchCalls atomic.Int32
	searchSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		searchCalls.Add(1)
		writeTestBody(t, w, `{"results":[{"title":"T","url":"https://e.test/1","description":"S"}]}`)
	}))
	defer searchSrv.Close()
	scfg := webConfig()
	scfg.Engines = []string{"brave"}
	scfg.Key = func(string) (string, error) { return "secret", nil }
	scfg.Base = map[string]string{"brave": searchSrv.URL}
	sm := startModule(t, scfg)
	root := session(t, "profile-root")
	raw := json.RawMessage(`{"query":"siblings","limit":3}`)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range 3 {
		view, err := root.Session.Child(fmt.Sprintf("child-%d", i), []string{"web_fetch"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		child := host.Env{Session: view}
		wg.Go(func() {
			<-start
			if _, err := sm.Invoke(context.Background(), "web_search", raw, child); err != nil {
				t.Errorf("child search: %v", err)
			}
		})
	}
	close(start)
	wg.Wait()
	if got := searchCalls.Load(); got != 1 {
		t.Fatalf("3 sibling subagents made %d vendor calls, want 1", got)
	}
	if _, err := sm.Invoke(context.Background(), "web_search", raw, session(t, "other-profile")); err != nil {
		t.Fatal(err)
	}
	if got := searchCalls.Load(); got != 2 {
		t.Fatalf("other profile shared a search hit: %d calls", got)
	}

	// Defaults are 20 minutes for search and 60 minutes for extract; the key
	// is only consulted after the SSRF/blocklist gate above.
	now := time.Unix(5000, 0)
	ttlCfg := webConfig()
	ttlCfg.now = func() time.Time { return now }
	ttl := startModule(t, ttlCfg)
	envTTL := session(t, "ttl")
	step := func(tool string, args json.RawMessage, advance time.Duration, want bool) {
		now = now.Add(advance)
		res, err := ttl.Invoke(context.Background(), tool, args, envTTL)
		if err != nil || res.Meta["cached"] != want {
			t.Fatalf("%s after %s: cached = %v err = %v, want %v", tool, advance, res.Meta["cached"], err, want)
		}
	}
	searchRaw := json.RawMessage(`{"query":"ttl","limit":1}`)
	step("web_search", searchRaw, 0, false)
	step("web_search", searchRaw, 19*time.Minute, true)
	step("web_search", searchRaw, 2*time.Minute, false)
	fetchRaw := json.RawMessage(fmt.Sprintf(`{"url":%q}`, srv.URL))
	step("web_fetch", fetchRaw, 0, false)
	step("web_fetch", fetchRaw, 59*time.Minute, true)
	step("web_fetch", fetchRaw, 2*time.Minute, false)
}

func TestWebSearchCache(t *testing.T) {
	t.Parallel()
	m := startModule(t, webConfig())
	a, b := session(t, "s1"), session(t, "s2")
	raw := json.RawMessage(`{"query":"golang","limit":2}`)
	if res, err := m.Invoke(context.Background(), "web_search", raw, a); err != nil || res.Meta["cached"] != false {
		t.Fatalf("first search = %+v %v", res.Meta, err)
	}
	if res, err := m.Invoke(context.Background(), "web_search", raw, a); err != nil || res.Meta["cached"] != true {
		t.Fatalf("cached search = %+v %v", res.Meta, err)
	}
	if res, err := m.Invoke(context.Background(), "web_search", raw, b); err != nil || res.Meta["cached"] != false {
		t.Fatalf("tenant b search = %+v %v", res.Meta, err)
	}
}

func session(t *testing.T, id string) host.Env {
	t.Helper()
	view, err := host.NewSessionView(id, "", "test", "/tmp", []string{"web_fetch"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return host.Env{Session: view}
}

func TestModuleBlocklist(t *testing.T) {
	t.Parallel()
	cfg := webConfig()
	cfg.blocklist = []string{"127.0.0.1/private", "blocked.invalid"}
	m := startModule(t, cfg)
	for _, url := range []string{"https://127.0.0.1/private/x", "https://sub.blocked.invalid/"} {
		_, err := m.Invoke(context.Background(), "web_fetch", json.RawMessage(`{"url":"`+url+`"}`), host.Env{})
		wantCode(t, err, "blocked_host")
	}
	if m.blocked("https://127.0.0.1/public") {
		t.Error("path outside the prefix was blocked")
	}
}
