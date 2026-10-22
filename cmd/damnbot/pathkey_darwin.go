//go:build darwin

package main

import "strings"

// monetaryKey folds case: the default APFS volume is case-insensitive.
func monetaryKey(p string) string { return strings.ToLower(canonicalBase(p)) }
