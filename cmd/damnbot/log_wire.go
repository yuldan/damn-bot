package main

import (
	"io"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/obs"
)

// serveLog installs the content-free logger for the daemon and returns its
// close function. A log-file failure is reported on stderr and does not take
// the daemon down; the data-dir lease and DB open fail closed separately when
// the directory is unusable.
func serveLog(dataDir string, cfg *config.Config, stderr io.Writer) func() {
	stop, err := obs.SetupLog(dataDir, "serve", cfg.Log.Level, cfg.Log.MessageContent, cfg.Log.MessageContentTTL.Std())
	if err != nil {
		sessionsPrintf(stderr, "serve: log: %s\n", cleanLine(err.Error()))
		return func() {}
	}
	return func() {
		if cerr := stop(); cerr != nil {
			sessionsPrintf(stderr, "serve: close log: %s\n", cleanLine(cerr.Error()))
		}
	}
}
