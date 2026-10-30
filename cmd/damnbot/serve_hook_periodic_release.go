//go:build !servetest

package main

import "io"

// registerServePeriodic is the release no-op: AUTO-029 runs only its
// production task (scheduled gc), never a synthetic one.
func registerServePeriodic(*serveCron, io.Writer) {}
