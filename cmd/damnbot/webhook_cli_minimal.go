//go:build minimal

package main

import "io"

func webhookUsage(w io.Writer) {
	sessionsPrintf(w, "usage: damnbot webhook <subscribe|list|remove|test> (unavailable in the minimal build)\n")
}

func runWebhook(_ []string, _ io.Writer, stderr io.Writer) int {
	sessionsPrintf(stderr, "webhook: subscriptions are unavailable in the minimal build\n")
	return 2
}
