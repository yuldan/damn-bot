//go:build !minimal

package webtools

import (
	"context"
	"errors"
	"testing"
)

func TestRegistryClosedSet(t *testing.T) {
	t.Parallel()
	if got := len(knownEngines()); got != 11 {
		t.Fatalf("production engines = %d, want the SPEC §9.4 eleven", got)
	}
	cfg := backendConfig{client: testClient(), key: func(string) (string, error) { return "secret", nil }}
	for _, id := range append(knownEngines(), "fake") {
		if _, err := newRegistry([]string{id}, cfg); err != nil {
			t.Errorf("known engine %q: %v", id, err)
		}
	}
	for _, id := range []string{"", "bing", "openai_native", "Brave", "nope"} {
		if _, err := newRegistry([]string{id}, cfg); !errors.Is(err, errUnknownEngine) {
			t.Errorf("unknown engine %q: err = %v", id, err)
		}
	}
}

func TestRegistrySearch(t *testing.T) {
	t.Parallel()
	reg, err := newRegistry([]string{"fake"}, backendConfig{})
	if err != nil {
		t.Fatal(err)
	}
	hits, engine, err := reg.Search(context.Background(), "go", 2)
	if err != nil || engine != "fake" || len(hits) != 2 {
		t.Fatalf("hits = %v engine=%q err=%v", hits, engine, err)
	}
	for _, hit := range hits {
		if hit.Title == "" || hit.URL == "" || hit.Snippet != "go" {
			t.Errorf("hit = %+v", hit)
		}
	}
}

func TestRegistryEmptyAndCancel(t *testing.T) {
	t.Parallel()
	reg, err := newRegistry(nil, backendConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, serr := reg.Search(context.Background(), "x", 1); !errors.Is(serr, errNoEngine) {
		t.Errorf("empty err = %v", serr)
	}
	reg, err = newRegistry([]string{"fake"}, backendConfig{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := reg.Search(ctx, "x", 1); !errors.Is(err, context.Canceled) {
		t.Errorf("cancel err = %v", err)
	}
}
