//go:build minimal

package main

import "context"

// The generic webhook receiver is a full-build surface; the minimal binary
// keeps its size budget (ADR-325) and starts no callback listener.
func (g *serveGateway) startGenericWebhook(context.Context) error { return nil }

func (g *serveGateway) stopGenericWebhook(context.Context) {}
