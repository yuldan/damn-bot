package main

import (
	"os"
	"strings"
)

// noColorMode is the single UX-06 decision: --plain, a non-empty NO_COLOR,
// TERM=dumb, or a stdout that is not a terminal all select the plain renderer.
func noColorMode(plain bool, noColorEnv, term string, tty bool) bool {
	if plain || noColorEnv != "" || strings.EqualFold(term, "dumb") {
		return true
	}
	return !tty
}

func stdoutIsTTY() bool {
	info, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
