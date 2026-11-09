//go:build minimal

package main

import (
	"context"

	"github.com/damnbot/damnbot/internal/assemble"
	"github.com/damnbot/damnbot/internal/compaction"
	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/loop"
)

func (a *assemblerAdapter) attachImages(context.Context, []compaction.Message, int, []loop.Message) error {
	return nil
}

// memoryHook is empty in the minimal build: no provider is compiled in.
type memoryHook struct{}

func (*memoryHook) mergeExternal(_ context.Context, _ string, _ *[]assemble.Message, hits []assemble.Snippet) []assemble.Snippet {
	return hits
}

// externalMemoryEnabled is false so the compiler folds the wiring out of the
// minimal binary.
const externalMemoryEnabled = false

func attachMemoryHook(context.Context, *config.Config, loop.Provider, string, *assemblerAdapter) error {
	return nil
}
