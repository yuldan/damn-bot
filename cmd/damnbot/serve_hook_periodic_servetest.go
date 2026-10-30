//go:build servetest

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	configos "github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/cron/periodic"
)

// registerServePeriodic makes AUTO-029 observable from the e2e harness:
// DAMNBOT_SERVE_TEST_PERIODIC is the task interval and DAMNBOT_SERVE_TEST_STATE
// the directory that receives periodic-runs (one line per run). FirstRun 0
// means the first serve tick runs it; the persisted slot keeps a restart from
// running it again. Compiled only with the `servetest` tag.
func registerServePeriodic(cr *serveCron, w io.Writer) {
	raw := configos.OS().Getenv("DAMNBOT_SERVE_TEST_PERIODIC")
	if raw == "" || cr == nil || cr.periodic == nil {
		return
	}
	interval, err := time.ParseDuration(raw)
	if err != nil || interval <= 0 {
		sessionsPrintf(w, "servetest: bad DAMNBOT_SERVE_TEST_PERIODIC=%q\n", raw)
		return
	}
	state := configos.OS().Getenv("DAMNBOT_SERVE_TEST_STATE")
	if state == "" {
		sessionsPrintf(w, "servetest: DAMNBOT_SERVE_TEST_PERIODIC needs DAMNBOT_SERVE_TEST_STATE\n")
		return
	}
	task := periodic.Task{
		Name: "servetest-periodic", Interval: interval, Budget: 5 * time.Second,
		Run: func(context.Context) (string, error) {
			f, oerr := os.OpenFile(filepath.Join(state, "periodic-runs"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
			if oerr != nil {
				return "", fmt.Errorf("periodic sentinel open: %w", oerr)
			}
			_, werr := f.WriteString(time.Now().UTC().Format(time.RFC3339Nano) + "\n")
			cerr := f.Close()
			if werr != nil {
				return "", fmt.Errorf("periodic sentinel write: %w", werr)
			}
			if cerr != nil {
				return "", fmt.Errorf("periodic sentinel close: %w", cerr)
			}
			return "", nil
		},
	}
	if rerr := cr.periodic.Register(task); rerr != nil {
		sessionsPrintf(w, "servetest: periodic register: %s\n", cleanLine(rerr.Error()))
		return
	}
	sessionsPrintf(w, "servetest: periodic task registered (%s)\n", interval)
}
