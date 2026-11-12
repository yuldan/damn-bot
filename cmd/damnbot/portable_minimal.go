//go:build minimal

package main

import (
	"context"
	"errors"
	"io"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/secret"
)

func exportPortable(context.Context, config.Paths, string, secret.BackupKey, ctxdb.Codec) (portableCounts, string, error) {
	return portableCounts{}, "", errors.New("portable archive is unavailable in the minimal build")
}

func runImportArchive(_ []string, _, stderr io.Writer) int {
	say(stderr, "import archive: unavailable in the minimal build\n")
	return 3
}
