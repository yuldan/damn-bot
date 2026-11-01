//go:build !minimal

package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/provider/registry"
)

func keyRef(name string) string { return "keychain:" + name }

func customPoolConfig() *config.Config {
	return &config.Config{Providers: config.Providers{Custom: []config.CustomProvider{{
		Name:    "k",
		BaseURL: "https://k.example/v1",
		API:     "openai-chat",
		Models:  []config.CustomModel{{ID: "m"}},
		Credentials: []config.ProviderCredential{
			{ID: "one", Secret: keyRef("k-one")},
			{ID: "two", Secret: keyRef("k-two")},
		},
	}}}}
}

func TestResolveCredentialPool(t *testing.T) {
	t.Parallel()
	cat, err := registry.DefaultCatalog()
	if err != nil {
		t.Fatal(err)
	}
	r := newResolver(customPoolConfig(), registry.PriceTable{}, cat, &runOptions{})
	r.secretGet = func(_ context.Context, name string) (string, error) { return "sk-" + name, nil }
	route, err := r.resolve(t.Context(), "k/m")
	if err != nil {
		t.Fatal(err)
	}
	if route.Pool == nil || route.APIKey != "" {
		t.Fatalf("route = %+v", route)
	}
	c1, ok := route.Pool.Acquire(time.Now())
	if !ok || c1.ID != "one" || c1.Secret() != "sk-k-one" {
		t.Fatalf("credential = %+v/%t", c1, ok)
	}
	second, err := r.resolve(t.Context(), "k/m")
	if err != nil {
		t.Fatal(err)
	}
	if second.Pool != route.Pool {
		t.Fatal("each route built its own pool; cooldowns would not be provider-wide")
	}
}

func TestResolveCredentialPoolMissingSecret(t *testing.T) {
	t.Parallel()
	cat, err := registry.DefaultCatalog()
	if err != nil {
		t.Fatal(err)
	}
	r := newResolver(customPoolConfig(), registry.PriceTable{}, cat, &runOptions{})
	r.secretGet = func(context.Context, string) (string, error) { return "", errors.New("keychain: key not found") }
	_, err = r.resolve(t.Context(), "k/m")
	if !errors.Is(err, ErrProviderResolve) || !strings.Contains(err.Error(), `credentials["one"]`) {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), "sk-") {
		t.Fatalf("error leaked a value: %v", err)
	}
}
