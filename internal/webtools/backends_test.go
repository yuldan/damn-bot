//go:build !minimal

package webtools

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func backendCfg(srv *httptest.Server) backendConfig {
	return backendConfig{
		client: testClient(),
		key:    func(string) (string, error) { return "secret", nil },
		base:   map[string]string{"brave": srv.URL, "tavily": srv.URL, "exa": srv.URL},
	}
}

func TestBraveBackend(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/res/v1/web/search" || r.URL.Query().Get("q") != "golang" ||
			r.URL.Query().Get("count") != "2" || r.Header.Get("X-Subscription-Token") != "secret" {
			t.Errorf("request = %s?%s headers=%v", r.URL.Path, r.URL.RawQuery, r.Header)
		}
		if _, err := w.Write([]byte(`{"web":{"results":[{"title":"T","url":"https://e.test/1","description":"D"}]}}`)); err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()
	hits, err := braveBackend{keyedBackend{id: "brave", client: testClient(), key: backendCfg(srv).key, base: srv.URL}}.
		Search(context.Background(), "golang", 2)
	if err != nil || len(hits) != 1 || hits[0].Title != "T" || hits[0].Snippet != "D" {
		t.Fatalf("hits = %+v, err = %v", hits, err)
	}
}

func searchBackend(t *testing.T, id, reply, want string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/search" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["query"] != "golang" {
			t.Errorf("body = %v", body)
		}
		if id == "tavily" && body["api_key"] != "secret" {
			t.Errorf("tavily key = %v", body["api_key"])
		}
		if id == "exa" && r.Header.Get("x-api-key") != "secret" {
			t.Errorf("exa header = %q", r.Header.Get("x-api-key"))
		}
		if _, err := w.Write([]byte(reply)); err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()
	kb := keyedBackend{id: id, client: testClient(), key: backendCfg(srv).key, base: srv.URL}
	var hits []searchResult
	var err error
	if id == "tavily" {
		hits, err = tavilyBackend{kb}.Search(context.Background(), "golang", 1)
	} else {
		hits, err = exaBackend{kb}.Search(context.Background(), "golang", 1)
	}
	if err != nil || len(hits) != 1 || hits[0].Snippet != want {
		t.Fatalf("hits = %+v, err = %v", hits, err)
	}
}

func TestTavilyBackend(t *testing.T) {
	t.Parallel()
	searchBackend(t, "tavily", `{"results":[{"title":"T","url":"https://e.test","content":"C"}]}`, "C")
}

func TestExaBackend(t *testing.T) {
	t.Parallel()
	searchBackend(t, "exa", `{"results":[{"title":"T","url":"https://e.test","text":"X"}]}`, "X")
}

func TestBackendCredentialAndFallback(t *testing.T) {
	t.Parallel()
	reg, err := newRegistry([]string{"brave"}, backendConfig{client: testClient()})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, serr := reg.Search(context.Background(), "x", 1); !errors.Is(serr, errEngineUnavailable) {
		t.Fatalf("no-key err = %v", serr)
	}
	reg, err = newRegistry([]string{"brave", "fake"}, backendConfig{client: testClient()})
	if err != nil {
		t.Fatal(err)
	}
	hits, engine, err := reg.Search(context.Background(), "x", 1)
	if err != nil || engine != "fake" || len(hits) != 1 {
		t.Fatalf("fallback = %+v %q %v", hits, engine, err)
	}
}
