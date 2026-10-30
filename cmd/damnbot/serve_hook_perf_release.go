//go:build !servetest

package main

import (
	"context"
	"io"
)

// registerServePerf is the release no-op: the F4 cron load and the
// negative-control load exist only in `servetest` builds (see
// serve_hook_perf_servetest.go).
func registerServePerf(context.Context, string, io.Writer) func() { return func() {} }
