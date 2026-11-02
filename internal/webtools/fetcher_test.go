//go:build !minimal

package webtools

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/netx"
)

func testClient() *netx.Client { return netx.New(netx.Config{}).Client("webtools.test", netx.Tool) }

// allowLoopback is the test-only host policy; production uses publicHost.
func allowLoopback(string) error { return nil }

func testFetcher(t *testing.T, cfg fetchConfig) *fetcher {
	t.Helper()
	cfg.UserAgent = "damnbot/test"
	if cfg.policy == nil {
		cfg.policy = allowLoopback
	}
	f, err := newFetcher(testClient(), cfg)
	if err != nil {
		t.Fatalf("newFetcher: %v", err)
	}
	return f
}

func TestFetcherSuccess(t *testing.T) {
	t.Parallel()
	var gotUA, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA, gotAccept = r.UserAgent(), r.Header.Get("Accept")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if _, err := io.WriteString(w, "<h1>ok</h1>"); err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()
	page, err := testFetcher(t, fetchConfig{}).Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if string(page.Body) != "<h1>ok</h1>" || page.Host != "127.0.0.1" || page.Status != 200 {
		t.Fatalf("page = %+v", page)
	}
	if gotUA != "damnbot/test" || !strings.Contains(gotAccept, "text/html") {
		t.Fatalf("headers: ua=%q accept=%q", gotUA, gotAccept)
	}
}

func TestFetcherSameHostRedirect(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/a" {
			http.Redirect(w, r, "/b", http.StatusFound)
			return
		}
		if _, err := io.WriteString(w, "final"); err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()
	page, err := testFetcher(t, fetchConfig{}).Fetch(context.Background(), srv.URL+"/a")
	if err != nil || string(page.Body) != "final" {
		t.Fatalf("Fetch = (%q, %v)", page.Body, err)
	}
}

func TestFetcherCrossHostRedirectRefused(t *testing.T) {
	t.Parallel()
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("cross-host redirect target was fetched")
		if _, err := io.WriteString(w, "leak"); err != nil {
			t.Error(err)
		}
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	defer srv.Close()
	_, err := testFetcher(t, fetchConfig{}).Fetch(context.Background(), srv.URL)
	if !errors.Is(err, errRedirect) {
		t.Fatalf("err = %v, want errRedirect", err)
	}
}

func TestFetcherPolicyRefusals(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	if _, err := testFetcher(t, fetchConfig{}).Fetch(ctx, "ftp://example.test/x"); !errors.Is(err, errScheme) {
		t.Errorf("scheme err = %v", err)
	}
	if _, err := testFetcher(t, fetchConfig{}).Fetch(ctx, "http://u:p@example.test/"); !errors.Is(err, errBadURL) {
		t.Errorf("userinfo err = %v", err)
	}
	if _, err := testFetcher(t, fetchConfig{Offline: true}).Fetch(ctx, "http://127.0.0.1:1/"); !errors.Is(err, errOffline) {
		t.Errorf("offline err = %v", err)
	}
	for _, host := range []string{"http://127.0.0.1:1/", "http://localhost:1/", "http://10.0.0.1/", "http://169.254.169.254/"} {
		f, err := newFetcher(testClient(), fetchConfig{UserAgent: "damnbot/test"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Fetch(ctx, host); !errors.Is(err, errBlockedHost) {
			t.Errorf("Fetch(%s) err = %v, want errBlockedHost", host, err)
		}
	}
}

func TestFetcherSizeAndStatus(t *testing.T) {
	t.Parallel()
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.WriteString(w, strings.Repeat("x", 64)); err != nil {
			t.Error(err)
		}
	}))
	defer big.Close()
	if _, err := testFetcher(t, fetchConfig{MaxBody: 8}).Fetch(context.Background(), big.URL); !errors.Is(err, errTooLarge) {
		t.Errorf("oversize err = %v", err)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer bad.Close()
	if _, err := testFetcher(t, fetchConfig{}).Fetch(context.Background(), bad.URL); !errors.Is(err, errHTTPStatus) {
		t.Errorf("status err = %v", err)
	}
}

func TestFetcherTimeout(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer srv.Close()
	_, err := testFetcher(t, fetchConfig{Timeout: 20 * time.Millisecond}).Fetch(context.Background(), srv.URL)
	if !errors.Is(err, errTimeout) {
		t.Fatalf("err = %v, want errTimeout", err)
	}
}

func TestNewFetcherConfig(t *testing.T) {
	t.Parallel()
	if _, err := newFetcher(nil, fetchConfig{UserAgent: "damnbot/test"}); !errors.Is(err, errBadConfig) {
		t.Errorf("nil client err = %v", err)
	}
	for _, ua := range []string{"", "curl/8", "damnbot/x y"} {
		if _, err := newFetcher(testClient(), fetchConfig{UserAgent: ua}); !errors.Is(err, errBadConfig) {
			t.Errorf("ua %q err = %v", ua, err)
		}
	}
	if _, err := newFetcher(testClient(), fetchConfig{UserAgent: "damnbot/test", MaxBody: -1}); !errors.Is(err, errBadConfig) {
		t.Errorf("max body err = %v", err)
	}
}
