//go:build minimal

package main

import (
	"context"
	"errors"
	"flag"
	"io"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
)

// errWebMinimal keeps the web UI out of the P13 minimal binary: the listener,
// its assets and its SSE stream are `!minimal` only.
var errWebMinimal = errors.New("web UI not built in minimal binary")

// webAvailable reports whether this build links the web UI.
func webAvailable() bool { return false }

// registerWebFlag declares `--web` so `serve --web` fails with a typed refusal
// instead of an unknown-flag message.
func registerWebFlag(fs *flag.FlagSet, web *bool) {
	fs.BoolVar(web, "web", false, "web UI (not available in the minimal build)")
}

// startServeWeb refuses in the minimal build (SIZE P13).
func startServeWeb(_ context.Context, _ *config.Config, _ *serveOptions, _ string, _ *ctxdb.Store, _, _ io.Writer) (func(context.Context) error, error) {
	return nil, errWebMinimal
}
