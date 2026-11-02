//go:build minimal

package main

import (
	"testing"

	"github.com/damnbot/damnbot/internal/config"
)

func TestMinimalAdapterHasNoPool(t *testing.T) {
	t.Parallel()
	p := loop.Provider(&providerAdapter{model: "k/m"})
	if _, ok := p.(*providerAdapter); !ok {
		t.Fatalf("minimal adapter = %T, want *providerAdapter", p)
	}
}
