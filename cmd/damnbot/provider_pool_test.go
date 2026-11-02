//go:build !minimal

package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/provider"
)

func poolConfig() *config.Config {
	return &config.Config{Providers: config.Providers{Custom: []config.CustomProvider{{
		Name: "k", BaseURL: "https://k.example/v1", API: "openai-chat",
		Models: []config.CustomModel{{ID: "m1"}, {ID: "m2"}},
		Credentials: []config.ProviderCredential{
			{ID: "a", Secret: "keychain:ka"}, {ID: "b", Secret: "keychain:kb"},
		},
	}}}}
}

func TestProviderPoolsSharedPerProvider(t *testing.T) {
	t.Parallel()
	pools, err := providerPoolsWith(poolConfig(), func(name string) (string, error) { return "sk-" + name, nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(pools) != 1 || pools["k"] == nil {
		t.Fatalf("pools = %v", pools)
	}
	c1, _ := pools["k"].Acquire(time.Now())
	c2, _ := pools["k"].Acquire(time.Now())
	if c1.ID == c2.ID {
		t.Fatalf("pool did not rotate: %q %q", c1.ID, c2.ID)
	}
}

type poolWire struct {
	mu     sync.Mutex
	keys   []string
	status []int
}

func (w *poolWire) handler(t *testing.T) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		w.mu.Lock()
		w.keys = append(w.keys, key)
		n := len(w.keys)
		w.mu.Unlock()
		status := http.StatusOK
		if n <= len(w.status) {
			status = w.status[n-1]
		}
		if status != http.StatusOK {
			rw.WriteHeader(status)
			if _, err := io.WriteString(rw, `{"error":{"message":"fake","code":"invalid_api_key"}}`); err != nil {
				t.Errorf("write: %v", err)
			}
			return
		}
		rw.Header().Set("Content-Type", "text/event-stream")
		if _, err := io.WriteString(rw, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"); err != nil {
			t.Errorf("write stream: %v", err)
		}
	}
}

func pooledTestClient(t *testing.T, w *poolWire, fallback []string) *provider.Pooled {
	t.Helper()
	srv := httptest.NewServer(w.handler(t))
	t.Cleanup(srv.Close)
	cfg := poolConfig()
	pools, err := providerPoolsWith(cfg, func(name string) (string, error) { return "sk-" + name, nil })
	if err != nil {
		t.Fatal(err)
	}
	rt1 := provider.Route{Provider: "k", Model: "m1", BaseURL: srv.URL}
	rt2 := provider.Route{Provider: "k", Model: "m2", BaseURL: srv.URL}
	n := netx.New(netx.Config{})
	t.Cleanup(n.CloseIdleConnections)
	base, err := provider.NewOpenAI(n.Client("provider", netx.Inference), &provider.Config{
		Routes: []provider.Route{rt1, rt2}, Fallback: fallback,
	})
	if err != nil {
		t.Fatal(err)
	}
	pooled, err := provider.NewPooled(base, pools, nil)
	if err != nil {
		t.Fatal(err)
	}
	return pooled
}

func doModel(t *testing.T, p *provider.Pooled, model string) {
	t.Helper()
	if _, err := p.Do(context.Background(), provider.PurposeTurn, &provider.Request{
		Model: model, Messages: []provider.Message{{Role: "user", Content: "hi"}},
	}, nil); err != nil {
		t.Fatalf("%s: %v", model, err)
	}
}

func TestPoolSharedAcrossModels(t *testing.T) {
	t.Parallel()
	w := &poolWire{status: []int{401, 200, 200}}
	p := pooledTestClient(t, w, nil)
	doModel(t, p, "k/m1")
	doModel(t, p, "k/m2")
	w.mu.Lock()
	keys := append([]string(nil), w.keys...)
	w.mu.Unlock()
	if len(keys) != 3 || keys[0] != "sk-ka" || keys[1] != "sk-kb" || keys[2] != "sk-kb" {
		t.Fatalf("keys = %v", keys)
	}
}

func TestPoolSharedWithFallback(t *testing.T) {
	t.Parallel()
	w := &poolWire{status: []int{503, 503, 503, 401, 200}}
	p := pooledTestClient(t, w, []string{"k/m2"})
	doModel(t, p, "k/m1")
	w.mu.Lock()
	keys := append([]string(nil), w.keys...)
	w.mu.Unlock()
	if len(keys) != 5 || keys[3] != "sk-kb" || keys[4] != "sk-ka" {
		t.Fatalf("fallback keys = %v", keys)
	}
}
