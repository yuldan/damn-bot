//go:build !minimal

package main

import (
	"context"
	"errors"
	"testing"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/memoryproviders"
)

type fakeRecall struct {
	hits []memoryproviders.Snippet
	err  error
}

func (f fakeRecall) Recall(context.Context, string) ([]memoryproviders.Snippet, error) {
	return f.hits, f.err
}

// TestF8MEM005ExternalProvider: the default configuration creates no provider
// and reads no credential; a foreign name is rejected; hits map to the
// assembler as raw, trusted-label data.
func TestF8MEM005ExternalProvider(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	ext, err := newExternalMemory(t.Context(), &cfg, nil, "")
	if err != nil || ext != nil {
		t.Fatalf("default off = %v, %v; want nil, nil", ext, err)
	}
	cfg.Modules.Memory.Provider = "byterover"
	if _, err = newExternalMemory(t.Context(), &cfg, nil, ""); !errors.Is(err, memoryproviders.ErrUnknownProvider) {
		t.Fatalf("foreign provider = %v, want ErrUnknownProvider", err)
	}
	adapter := &memoryAdapter{client: fakeRecall{hits: []memoryproviders.Snippet{{Source: "honcho", Text: "note"}}}}
	hits, err := adapter.Recall(t.Context(), "q")
	if err != nil || len(hits) != 1 || hits[0].Source != "honcho" || hits[0].Text != "note" {
		t.Fatalf("adapter = %+v, %v", hits, err)
	}
}
