package main

import (
	"context"
	"fmt"
	"io"
	"runtime/debug"

	"github.com/damnbot/damnbot/internal/redact"
)

// applyResumeNotes prints the resume recap and resolves --retry to the last
// user message before the new turn starts.
func applyResumeNotes(ctx context.Context, o *runOptions, model *runModel, red *redact.Redactor, stderr io.Writer) error {
	if (o.session != "" || o.cont) && !o.noRecap {
		if err := printRecap(ctx, model.db, model.sessionID, red, stderr); err != nil {
			recapPrintf(stderr, "run: recap: %v\n", err)
		}
	}
	if !o.retry {
		return nil
	}
	prompt, err := retryPrompt(ctx, model.db, model.sessionID, red, stderr)
	if err != nil {
		return fmt.Errorf("retry: %w", err)
	}
	o.prompt = prompt
	return nil
}

// trimRSS releases the run's freed pages once the turn is over (MEM-033). The
// hook is a test seam; nil falls back to debug.FreeOSMemory so the normal path
// never gains a panic site.
func trimRSS(o *runOptions) {
	trim := o.trimMemory
	if trim == nil {
		trim = debug.FreeOSMemory
	}
	trim()
}
