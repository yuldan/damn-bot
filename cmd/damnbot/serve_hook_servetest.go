//go:build servetest

package main

import (
	configos "github.com/damnbot/damnbot/internal/config"
	"io"
	"os"
	"path/filepath"
	"time"
)

// registerServeTestTurn makes the drain path observable from an end-to-end
// test: DAMNBOT_SERVE_TEST_TURN registers one synthetic turn that ends after
// the duration and reports whether the daemon cancelled it meanwhile.
//
// DAMNBOT_SERVE_TEST_STATE turns that report into filesystem sentinels
// (turn-started, turn-completed, turn-cancelled). Each marker is written
// before the turn releases its slot, so a test that sees the file can also
// trust that the process will not exit before the state is observable; it
// replaces log polling and elapsed-time guesses.
// Compiled only with the `servetest` tag, never into a release binary.
func registerServeTestTurn(d *serveDaemon, w io.Writer) {
	raw := configos.OS().Getenv("DAMNBOT_SERVE_TEST_TURN")
	if raw == "" {
		return
	}
	state := configos.OS().Getenv("DAMNBOT_SERVE_TEST_STATE")
	dur, err := time.ParseDuration(raw)
	if err != nil || dur <= 0 {
		sessionsPrintf(w, "servetest: bad DAMNBOT_SERVE_TEST_TURN=%q\n", raw)
		return
	}
	ctx, end, ok := d.BeginTurn()
	if !ok {
		sessionsPrintf(w, "servetest: daemon already draining\n")
		return
	}
	markTurnState(w, state, "turn-started")
	sessionsPrintf(w, "servetest: turn started (%s)\n", dur)
	time.AfterFunc(dur, func() {
		status := "turn-completed"
		if ctx.Err() != nil {
			status = "turn-cancelled"
		}
		markTurnState(w, state, status)
		sessionsPrintf(w, "servetest: turn %s\n", status[len("turn-"):])
		end()
	})
}

// markTurnState writes one sentinel. Errors are reported, never fatal: the
// waiting test fails on the missing file with the child's stderr.
func markTurnState(w io.Writer, state, name string) {
	if state == "" {
		return
	}
	if err := os.MkdirAll(state, 0o700); err != nil {
		sessionsPrintf(w, "servetest: state dir: %s\n", cleanLine(err.Error()))
		return
	}
	if err := os.WriteFile(filepath.Join(state, name), []byte(name), 0o600); err != nil {
		sessionsPrintf(w, "servetest: state %s: %s\n", name, cleanLine(err.Error()))
	}
}
