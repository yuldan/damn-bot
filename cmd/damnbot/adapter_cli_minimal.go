//go:build minimal

package main

import "io"

// The adapter setup helpers are full-build surfaces: the minimal binary keeps
// its size budget (ADR-325) and never links the setup writers. The commands
// stay in the registry so `damnbot help` is identical at every size.
func whatsappUsage(w io.Writer) {
	sessionsPrintf(w, "usage: damnbot whatsapp (not compiled in the minimal build)\n")
}

func runWhatsApp(_ []string, _, stderr io.Writer) int {
	sessionsPrintf(stderr, "whatsapp: not compiled in the minimal build\n")
	return 2
}

func whatsappCloudUsage(w io.Writer) {
	sessionsPrintf(w, "usage: damnbot whatsapp-cloud (not compiled in the minimal build)\n")
}

func runWhatsAppCloud(_ []string, _, stderr io.Writer) int {
	sessionsPrintf(stderr, "whatsapp-cloud: not compiled in the minimal build\n")
	return 2
}

func slackUsage(w io.Writer) {
	sessionsPrintf(w, "usage: damnbot slack (not compiled in the minimal build)\n")
}

func runSlack(_ []string, _, stderr io.Writer) int {
	sessionsPrintf(stderr, "slack: not compiled in the minimal build\n")
	return 2
}
