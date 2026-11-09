//go:build minimal

package main

import (
	"context"
	"io"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/obs"
)

// startServeOTLP is the minimal-build no-op: the OTLP exporter is excluded to
// keep the P13 binary envelope, and the default is off anyway.
func startServeOTLP(context.Context, *config.Config, *obs.Metrics, io.Writer) (func(), error) {
	return func() {}, nil
}
