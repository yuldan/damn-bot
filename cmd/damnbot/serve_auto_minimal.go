//go:build minimal

package main

import (
	"context"
	"io"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/loop"
)

// serveAuto is absent in the minimal binary: [modules.auto] is not compiled
// in (ADR-333). The field stays on serveGateway so the shared struct builds.
type serveAuto struct{}

func (*serveAuto) runTitle(context.Context, loop.Provider, *ctxdb.Store, string, string, string) {}

func (*serveGateway) autoTitleAfterTurn(context.Context, loop.Provider, string, string) {}

// startServeAuto is a no-op in the minimal binary.
func startServeAuto(context.Context, *config.Config, string, *serveGateway, io.Writer) {}
