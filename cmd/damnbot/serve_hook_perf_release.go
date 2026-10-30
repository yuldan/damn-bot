//go:build !servetest

package main

import (
	"context"
	"io"
)

// registerServePerf is the release no-op: the F4 cron tick and the
// negative-control load exist only in `servetest` builds (see
// serve_hook_perf_servetest.go). The production cron scheduler is wired by
// serveCronFor in every build.
func registerServePerf(context.Context, string, io.Writer) func() { return func() {} }
