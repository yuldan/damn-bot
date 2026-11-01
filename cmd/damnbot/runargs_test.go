package main

import (
	"bytes"
	"testing"
)

// runArgs is the in-process entry point every CLI test uses.
func runArgs(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = run(args, &out, &errb)
	return code, out.String(), errb.String()
}
