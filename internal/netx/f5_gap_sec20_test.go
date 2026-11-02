package netx

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestGAPF5_SEC20SameHostRedirects pins the catalog redirect policy: at most
// three same-host hops, and a cross-host Location is refused.
func TestGAPF5_SEC20SameHostRedirects(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/0":
			http.Redirect(w, r, "/1", http.StatusFound)
		case "/1":
			http.Redirect(w, r, "/2", http.StatusFound)
		case "/2":
			http.Redirect(w, r, "/3", http.StatusFound)
		case "/3":
			if _, err := io.WriteString(w, "ok"); err != nil {
				t.Errorf("write: %v", err)
			}
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	n := New(Config{})
	t.Cleanup(n.CloseIdleConnections)
	c := n.Client("catalog-test", Background).WithSameHostRedirects(3)
	code, _ := doTestGet(t, c, srv.URL+"/0")
	if code != http.StatusOK {
		t.Fatalf("three same-host redirects = %d, want 200", code)
	}

	// A fourth hop is not followed: /4→/5→/6→/7 are the allowed three, and
	// /7's Location to /8 is returned as a 302.
	for i := 4; i <= 7; i++ {
		next := "/" + string(rune('0'+i+1))
		mux.HandleFunc("/"+string(rune('0'+i)), func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, next, http.StatusFound)
		})
	}
	code, _ = doTestGet(t, c, srv.URL+"/4")
	if code != http.StatusFound {
		t.Fatalf("fourth redirect = %d, want 302 (not followed)", code)
	}

	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := io.WriteString(w, "evil"); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	t.Cleanup(other.Close)
	mux.HandleFunc("/cross", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	})
	code, body := doTestGet(t, c, srv.URL+"/cross")
	if code != http.StatusFound {
		t.Fatalf("cross-host = %d, want 302 (not followed)", code)
	}
	if strings.Contains(body, "evil") {
		t.Fatal("cross-host body fetched")
	}
}

func doTestGet(t *testing.T, c *Client, url string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, rerr := io.ReadAll(resp.Body)
	if cerr := resp.Body.Close(); cerr != nil {
		t.Errorf("close body: %v", cerr)
	}
	if rerr != nil {
		t.Fatal(rerr)
	}
	return resp.StatusCode, string(body)
}
