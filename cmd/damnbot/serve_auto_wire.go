//go:build !minimal

package main

import (
	"context"
	"io"

	"github.com/damnbot/damnbot/internal/config"
)

// startServeAuto builds and starts the automation lane. With every feature
// off (the default) it registers no task and makes no call; the goroutine
// still owns the injected clock and exits with ctx.
func startServeAuto(ctx context.Context, cfg *config.Config, dataDir string, gw *serveGateway, stderr io.Writer) {
	a, err := newServeAuto(cfg, dataDir, nil)
	if err != nil {
		sessionsPrintf(stderr, "serve: auto: %s\n", cleanLine(err.Error()))
		return
	}
	if gw != nil {
		gw.auto = a
	}
	go func() {
		if rerr := a.Run(ctx); rerr != nil {
			sessionsPrintf(stderr, "serve: auto: %s\n", cleanLine(rerr.Error()))
		}
	}()
}
