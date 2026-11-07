package email

import (
	"context"
	"fmt"
)

// Cursor is the IMAP position of one mailbox (GAP-GW-08): a UID is only valid
// inside one UIDVALIDITY generation.
type Cursor struct {
	UIDValidity uint32
	UID         uint32
}

// CursorStore persists the cursor across restarts. The composition root adapts
// the gateway_cursors row (internal/ctxdb/gwstore) to this seam.
type CursorStore interface {
	Load(ctx context.Context) (Cursor, bool, error)
	Save(ctx context.Context, cursor Cursor) error
}

// save persists the current position before the message is acknowledged, so a
// crash replays at most one message and the Message-ID dedupe drops the replay.
func (a *Adapter) saveCursor(ctx context.Context) error {
	if a.cfg.Cursors == nil {
		return nil
	}
	if err := a.cfg.Cursors.Save(ctx, a.cursor); err != nil {
		return fmt.Errorf("email: save cursor: %w", err)
	}
	return nil
}
