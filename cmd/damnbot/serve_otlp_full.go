//go:build !minimal

package main

import (
	"context"
	"fmt"
	"io"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/obs"
)

// startServeOTLP wires the opt-in OTLP exporter (OPS-018) into serve. The
// exporter is excluded from the minimal build so the P13 envelope keeps its
// headroom; a disabled exporter still opens no socket in the full build.
func startServeOTLP(ctx context.Context, cfg *config.Config, m *obs.Metrics, stderr io.Writer) (func(), error) {
	e, err := obs.NewOTLP(obs.OTLPConfig{Enabled: cfg.Telemetry.OTLPEnabled, Endpoint: cfg.Telemetry.OTLPEndpoint})
	if err != nil {
		return nil, fmt.Errorf("telemetry: %w", err)
	}
	if !e.Enabled() {
		return func() {}, nil
	}
	e.SetSampler(func() { m.Export(e) })
	e.Start(ctx)
	return func() {
		if err := e.Shutdown(context.WithoutCancel(ctx)); err != nil {
			sessionsPrintf(stderr, "serve: otlp: %s\n", cleanLine(err.Error()))
		}
	}, nil
}
