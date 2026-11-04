//go:build minimal

package main

import (
	"context"

	"github.com/damnbot/damnbot/internal/compaction"
	"github.com/damnbot/damnbot/internal/loop"
)

func (a *assemblerAdapter) attachImages(context.Context, []compaction.Message, int, []loop.Message) error {
	return nil
}
