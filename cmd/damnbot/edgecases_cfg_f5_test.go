//go:build !minimal

package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/provider"
	"github.com/damnbot/damnbot/internal/provider/credpool"
	"github.com/damnbot/damnbot/internal/testutil/phase"
)

const cfgPoolSSE = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\n" +
	"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
	"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1}}\n\n" +
	"data: [DONE]\n\n"

// cfgPoolServer records the credential of every request and answers with a
// minimal chat stream.
type cfgPoolServer struct {
	mu   sync.Mutex
	keys []string
	srv  *httptest.Server
}

func newCfgPoolServer(t *testing.T) *cfgPoolServer {
	t.Helper()
	s := &cfgPoolServer{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.keys = append(s.keys, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		s.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if _, err := io.WriteString(w, cfgPoolSSE); err != nil {
			t.Errorf("write stream: %v", err)
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *cfgPoolServer) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.keys...)
}

// cfgPoolProvider builds one custom provider with a keychain credential pool.
func cfgPoolProvider(name string, srv *cfgPoolServer, ids ...string) config.CustomProvider {
	creds := make([]config.ProviderCredential, len(ids))
	for i, id := range ids {
		creds[i] = config.ProviderCredential{ID: id, Secret: "keychain:" + id}
	}
	return config.CustomProvider{
		Name: name, BaseURL: srv.srv.URL, API: "openai-chat",
		Models: []config.CustomModel{{ID: "m"}}, Credentials: creds,
	}
}

// TestProviderSwitchLoadsProviderPool (EC-CFG-008): the pools the composition
// root builds from config are keyed by provider, so switching the active
// model to another provider selects that provider's credentials instead of
// reusing (or losing) the previous provider's pool. A provider without a
// declared pool keeps the single-credential path.
func TestProviderSwitchLoadsProviderPool(t *testing.T) {
	t.Parallel()
	g := phase.Current(t)
	if !g.Item(t, "EC-CFG-008", "F5", "covered") {
		return
	}
	t.Run("EC-CFG-008", func(t *testing.T) {
		t.Parallel()
		alpha, beta := newCfgPoolServer(t), newCfgPoolServer(t)
		cfg := config.Default()
		cfg.Providers.Custom = []config.CustomProvider{
			cfgPoolProvider("alpha", alpha, "alpha-1", "alpha-2"),
			cfgPoolProvider("beta", beta, "beta-1"),
		}
		pools := cfgPools(t, &cfg)
		pooled := cfgPooled(t, pools, alpha, beta)
		cfgCheckSteps(t, pooled, []cfgPoolStep{
			{"alpha/m", "alpha-1", "sk-alpha-1", alpha},
			{"beta/m", "beta-1", "sk-beta-1", beta},
		})
		if got := alpha.seen(); len(got) != 1 {
			t.Fatalf("switching provider touched alpha again: %v", got)
		}
		// Negative control: a route whose provider declares no pool stays on
		// its static credential, not on a neighbour's pool.
		resp, err := pooled.Do(t.Context(), provider.PurposeTurn, cfgAsk("plain/m"), nil)
		if err != nil || resp.CredentialID != "" {
			t.Fatalf("plain: resp=%+v err=%v", resp, err)
		}
		if got := beta.seen(); len(got) != 2 || got[1] != "sk-plain" {
			t.Fatalf("plain route keys = %v", got)
		}
	})
}

type cfgPoolStep struct {
	model, cred, key string
	server           *cfgPoolServer
}

// cfgPools loads the config-declared pools and pins that both providers have
// one; a switch must select from this map, not from a shared singleton.
func cfgPools(t *testing.T, cfg *config.Config) map[string]*credpool.Pool {
	t.Helper()
	pools, err := providerPoolsWith(cfg, func(id string) (string, error) { return "sk-" + id, nil })
	if err != nil {
		t.Fatalf("providerPoolsWith: %v", err)
	}
	if len(pools) != 2 || pools["alpha"] == nil || pools["beta"] == nil {
		t.Fatalf("pools = %v, want alpha and beta", pools)
	}
	return pools
}

func cfgCheckSteps(t *testing.T, pooled *provider.Pooled, steps []cfgPoolStep) {
	t.Helper()
	for _, st := range steps {
		resp, err := pooled.Do(t.Context(), provider.PurposeTurn, cfgAsk(st.model), nil)
		if err != nil || resp.CredentialID != st.cred {
			t.Fatalf("%s: resp=%+v err=%v", st.model, resp, err)
		}
		if got := st.server.seen(); len(got) != 1 || got[0] != st.key {
			t.Fatalf("%s keys = %v, want [%s]", st.model, got, st.key)
		}
	}
}

func cfgPooled(t *testing.T, pools map[string]*credpool.Pool, alpha, beta *cfgPoolServer) *provider.Pooled {
	t.Helper()
	n := netx.New(netx.Config{})
	t.Cleanup(n.CloseIdleConnections)
	base, err := provider.NewOpenAI(n.Client("cfg", netx.Inference), &provider.Config{Routes: []provider.Route{
		{Provider: "alpha", Model: "m", BaseURL: alpha.srv.URL, APIKey: "unused"},
		{Provider: "beta", Model: "m", BaseURL: beta.srv.URL, APIKey: "unused"},
		{Provider: "plain", Model: "m", BaseURL: beta.srv.URL, APIKey: "sk-plain"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	pooled, err := provider.NewPooled(base, pools, nil)
	if err != nil {
		t.Fatal(err)
	}
	return pooled
}

func cfgAsk(model string) *provider.Request {
	return &provider.Request{Model: model, Messages: []provider.Message{{Role: "user", Content: "hi"}}}
}
