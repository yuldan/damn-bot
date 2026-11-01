//go:build minimal

package main

import (
	"testing"

	"github.com/damnbot/damnbot/internal/config"
)

func TestMinimalAdapterHasNoPool(t *testing.T) {
	t.Parallel()
	p, err := newRunAdapter(nil, &config.Config{}, "k/m", "", nil, "k", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.(*providerAdapter); !ok {
		t.Fatalf("minimal adapter = %T, want *providerAdapter", p)
	}
}
