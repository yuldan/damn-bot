//go:build minimal

package main

import (
	"context"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/modules/host"
)

func newBrowserControl(*config.Config, host.Approvals) browserControl {
	return browserControlUnavailable{}
}

func (r *tuiRunner) BrowserStatus(ctx context.Context, sessionID string) string {
	return browserControlUnavailable{}.BrowserStatus(ctx, sessionID)
}

func (r *tuiRunner) BrowserConnect(ctx context.Context, sessionID, engine string) string {
	return browserControlUnavailable{}.BrowserConnect(ctx, sessionID, engine)
}

func (r *tuiRunner) BrowserClose(ctx context.Context, sessionID string) string {
	return browserControlUnavailable{}.BrowserClose(ctx, sessionID)
}

func (r *tuiRunner) BrowserEngine(ctx context.Context, sessionID, engine string) string {
	return browserControlUnavailable{}.BrowserEngine(ctx, sessionID, engine)
}

func browserSurface() []surfaceEntry { return nil }
