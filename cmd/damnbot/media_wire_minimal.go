//go:build minimal

package main

import (
	"github.com/damnbot/damnbot/internal/loop"
	"github.com/damnbot/damnbot/internal/modules/host"
)

// wireRunMedia is a no-op in the minimal build: the media module and its
// vision tools are full-only.
func wireRunMedia(func(host.Completer, bool, string), loop.Provider) {}
