//go:build !servetest

package main

import "io"

// registerServeTestTurn is absent from release builds: the daemon starts
// with no synthetic work.
func registerServeTestTurn(*serveDaemon, io.Writer) {}
