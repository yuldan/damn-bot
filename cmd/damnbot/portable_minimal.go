//go:build minimal

package main

import (
	"context"
	"errors"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/secret"
)

func exportPortable(context.Context, config.Paths, string, secret.BackupKey, ctxdb.Codec) (portableCounts, string, error) {
	return portableCounts{}, "", errors.New("portable archive is unavailable in the minimal build")
}
