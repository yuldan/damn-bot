//go:build minimal

package main

import (
	"context"
	"time"

	"github.com/damnbot/damnbot/internal/config"
)

// startupMaintenance is empty in the minimal build: [modules.auto] is not
// compiled in, so the start path never touches the filesystem (AUTO-031).
func startupMaintenance(context.Context, *config.Config, string, func() time.Time) error { return nil }
