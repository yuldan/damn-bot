//go:build !minimal

package webtools

import (
	"context"
	"errors"
	"testing"
)

func TestNewRegistryClosedSet(t *testing.T) {
	t.Parallel()
	reg, err := newRegistry([]string{"fake"})
	if err != nil || reg == nil {
		t.Fatalf("registry = %v, err = %v", reg, err)
	}
	if _, err := newRegistry([]string{"brave"}); !errors.Is(err, errUnknownEngine) {
		t.Errorf("unknown err = %v", err)
	}
}

func TestRegistrySearch(t *testing.T) {
	t.Parallel()
	reg, err := newRegistry([]string{"fake"})
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
	reg, err := newRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, serr := reg.Search(context.Background(), "x", 1); !errors.Is(serr, errNoEngine) {
		t.Errorf("empty err = %v", serr)
	}
	reg, err = newRegistry([]string{"fake"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := reg.Search(ctx, "x", 1); !errors.Is(err, context.Canceled) {
		t.Errorf("cancel err = %v", err)
	}
}
