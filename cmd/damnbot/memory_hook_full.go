//go:build !minimal

package main

import (
	"context"

	"github.com/damnbot/damnbot/internal/assemble"
	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/loop"
)

// externalMemory is the optional per-turn recall seam (MEM-005/008). It is nil
// when [modules.memory] is off, which is the default.
type externalMemory interface {
	Recall(ctx context.Context, query string) ([]assemble.Snippet, error)
}

// externalMemoryNotice marks a degraded recall in the request, so a provider
// failure is visible to the model instead of silently skipped (SPEC §9.10).
const externalMemoryNotice = "[memory provider unavailable; using local context only]"

const externalMemoryEnabled = true

type memoryHook struct {
	external externalMemory
}

func (h *memoryHook) mergeExternal(ctx context.Context, query string, history *[]assemble.Message, hits []assemble.Snippet) []assemble.Snippet {
	if h.external == nil {
		return hits
	}
	return append(hits, h.externalRecall(ctx, query, history)...)
}

func (h *memoryHook) externalRecall(ctx context.Context, query string, history *[]assemble.Message) []assemble.Snippet {
	extra, err := h.external.Recall(ctx, query)
	if err != nil {
		*history = append(*history, assemble.Message{Role: "user", Content: externalMemoryNotice})
		return nil
	}
	return extra
}

func attachMemoryHook(ctx context.Context, cfg *config.Config, prov loop.Provider, modelRef string, target *assemblerAdapter) error {
	ext, err := newExternalMemory(ctx, cfg, prov, modelRef)
	if err != nil {
		return err
	}
	target.external = ext
	return nil
}
