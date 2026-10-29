//go:build servetest

package main

import (
	"io"
	"os"
	"time"
)

// registerServeTestTurn makes the drain path observable from an end-to-end
// test: DAMNBOT_SERVE_TEST_TURN registers one synthetic turn that ends after
// the duration and reports whether the daemon cancelled it meanwhile.
// Compiled only with the `servetest` tag, never into a release binary.
func registerServeTestTurn(d *serveDaemon, w io.Writer) {
	raw := os.Getenv("DAMNBOT_SERVE_TEST_TURN")
	if raw == "" {
		return
	}
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
	sessionsPrintf(w, "servetest: turn started (%s)\n", dur)
	time.AfterFunc(dur, func() {
		end()
		if ctx.Err() != nil {
			sessionsPrintf(w, "servetest: turn cancelled\n")
			return
		}
		sessionsPrintf(w, "servetest: turn completed\n")
	})
}
