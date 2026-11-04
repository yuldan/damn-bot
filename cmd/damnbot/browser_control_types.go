package main

import "context"

var _ browserControl = browserControlUnavailable{}

// browserControl is the `/browser` surface seam shared by the TUI and gateway.
type browserControl interface {
	BrowserStatus(ctx context.Context, sessionID string) string
	BrowserConnect(ctx context.Context, sessionID, engine string) string
	BrowserClose(ctx context.Context, sessionID string) string
	BrowserEngine(ctx context.Context, sessionID, engine string) string
}

// browserControlUnavailable is the honest answer when no controller is wired.
type browserControlUnavailable struct{}

func (browserControlUnavailable) BrowserStatus(context.Context, string) string {
	return "browser: module_unavailable: browser module is not wired"
}

func (browserControlUnavailable) BrowserConnect(context.Context, string, string) string {
	return "browser: module_unavailable: browser module is not wired"
}

func (browserControlUnavailable) BrowserClose(context.Context, string) string {
	return "browser: module_unavailable: browser module is not wired"
}

func (browserControlUnavailable) BrowserEngine(context.Context, string, string) string {
	return "browser: module_unavailable: browser module is not wired"
}
