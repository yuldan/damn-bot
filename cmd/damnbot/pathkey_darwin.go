//go:build darwin

package main

import (
	"strings"

	"golang.org/x/text/unicode/norm"
)

// monetaryKey normalizes to NFC before folding case: the default APFS
// volume is case-insensitive and stores decomposed names (SEC10-16).
func monetaryKey(p string) string {
	return strings.ToLower(norm.NFC.String(canonicalBase(norm.NFC.String(p))))
}
