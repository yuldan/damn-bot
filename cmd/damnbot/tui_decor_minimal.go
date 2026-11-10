//go:build minimal

package main

import "github.com/damnbot/damnbot/internal/tui"

// newTUIDecor keeps the minimal build free of the pets/personality stores.
func newTUIDecor(string) tui.Decor { return nil }
