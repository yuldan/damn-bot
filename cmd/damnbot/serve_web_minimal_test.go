//go:build minimal

package main

import (
	"context"
	"errors"
	"testing"

	"github.com/damnbot/damnbot/internal/config"
)

// TestWebMinimalRefusal: the minimal binary keeps the web UI out but refuses
// `serve --web` with a typed, actionable error instead of an unknown flag.
func TestWebMinimalRefusal(t *testing.T) {
	t.Parallel()
	if webAvailable() {
		t.Fatal("minimal build links the web UI")
	}
	stop, err := startServeWeb(context.Background(), &config.Config{}, &serveOptions{web: true}, "", nil, nil, nil)
	if !errors.Is(err, errWebMinimal) || stop != nil {
		t.Fatalf("startServeWeb = %v, %v", stop, err)
	}
	if err.Error() != "web UI not built in minimal binary" {
		t.Fatalf("message = %q", err.Error())
	}
}
