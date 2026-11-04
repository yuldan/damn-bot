//go:build !minimal

package main

import (
	"context"
	"fmt"

	"github.com/damnbot/damnbot/internal/compaction"
	"github.com/damnbot/damnbot/internal/loop"
)

// attachImages resolves the image shas of user messages into current-turn
// bytes. A missing blob fails closed instead of silently sending a prompt
// without the screenshot the user attached.
func (a *assemblerAdapter) attachImages(ctx context.Context, hist []compaction.Message, lead int, out []loop.Message) error {
	for i := range hist {
		if hist[i].Role != "user" || len(hist[i].Images) == 0 {
			continue
		}
		at := lead + i
		if at < 0 || at >= len(out) {
			continue
		}
		for _, sha := range hist[i].Images {
			data, err := a.db.ReadBlob(ctx, sha)
			if err != nil {
				return fmt.Errorf("assembler: image blob %s: %w", sha, err)
			}
			out[at].Attachments = append(out[at].Attachments, loop.Attachment{
				Kind: "image", Name: "image", BlobSHA: sha, Size: int64(len(data)), Data: data,
			})
		}
	}
	return nil
}
