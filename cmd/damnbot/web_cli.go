package main

import (
	"io"
)

// runDashboard starts the web UI (SPEC §10 `damnbot dashboard`, CLI-068) by
// reusing the `serve --web` surface: one daemon, one auth model, embedded
// assets, 0 external URLs. In the minimal build `serve --web` refuses with the
// typed web refusal rather than serving a half dashboard.
func runDashboard(args []string, stdout, stderr io.Writer) int {
	return runServe(append([]string{"--web"}, args...), stdout, stderr)
}

// runDesktop is the `damnbot desktop` surface (CLI-070): the web UI replaces
// the Electron app, so it serves the same listener and prints the one-use URL.
// It never needs Node or Electron at runtime.
func runDesktop(args []string, stdout, stderr io.Writer) int {
	return runServe(append([]string{"--web"}, args...), stdout, stderr)
}

// dashboardUsage documents the command through the serve flag set.
func dashboardUsage(w io.Writer) {
	sessionsPrintf(w, "usage: damnbot dashboard [flags]\n\n")
	sessionsPrintf(w, "Starts the web UI dashboard on [web].listen (SPEC §13.2).\n\n")
	serveFlagSet(&serveOptions{}, w).PrintDefaults()
}

// desktopUsage documents the desktop wrapper replacement.
func desktopUsage(w io.Writer) {
	sessionsPrintf(w, "usage: damnbot desktop [flags]  (alias: gui)\n\n")
	sessionsPrintf(w, "Serves the web UI dashboard and prints its one-use URL; 0 Node/Electron runtime.\n\n")
	serveFlagSet(&serveOptions{}, w).PrintDefaults()
}
