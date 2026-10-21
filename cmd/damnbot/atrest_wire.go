// At-rest codec wiring for `damnbot run` (ADR-124). Storage encryption is
// opt-in, so the run path only touches the key store when the config asks for
// it; the codec is then threaded into the run wiring.
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/secret"
)

// runCodec returns the run store codec, or nil when storage.encrypt is off.
// A missing DB means "create": the key store provisions the KEK/DEK pair.
func runCodec(ctx context.Context, cfg *config.Config, dataDir, configDir string) (ctxdb.Codec, error) {
	if !cfg.Storage.Encrypt {
		return nil, nil
	}
	st, err := secret.New(secret.Options{ConfigDir: configDir})
	if err != nil {
		return nil, fmt.Errorf("at-rest key store: %w", err)
	}
	_, serr := os.Stat(ctxdb.DBPath(dataDir))
	create := errors.Is(serr, fs.ErrNotExist)
	codec, err := secret.OpenCodec(ctx, dataDir, st, create)
	if err != nil {
		return nil, fmt.Errorf("at-rest codec: %w", err)
	}
	return codec, nil
}
