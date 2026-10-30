package gwstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// Pending is one inbound row inside an emergency-stop replay window.
type Pending struct {
	Inbound
	Text string
}

// PendingInbound lists one page of the replay window.
func (s Store) PendingInbound(ctx context.Context, sinceMS, untilMS int64, limit int) ([]Pending, error) {
	var out []Pending
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		ids, err := tx.Strings(`SELECT i.platform || char(31) || i.external_id
			FROM gateway_inbound i
			WHERE i.received_at >= ?1 AND i.received_at <= ?2
			ORDER BY i.received_at, i.external_id LIMIT ?3`, sinceMS, untilMS, limit)
		if err != nil {
			return fmt.Errorf("pending inbound: %w", err)
		}
		for _, id := range ids {
			platform, updateID, ok := strings.Cut(id, "\x1f")
			if !ok {
				return fmt.Errorf("pending inbound: bad row id")
			}
			var p Pending
			var payload string
			var sealed []byte
			if err := tx.Get(`SELECT chat_id, thread_id, sender_id, coalesce(payload, '{}'),
				coalesce(payload_enc, x'') FROM gateway_inbound
				WHERE platform = ?1 AND external_id = ?2`, []any{platform, updateID},
				&p.ChatID, &p.ThreadID, &p.SenderID, &payload, &sealed); err != nil {
				return fmt.Errorf("pending inbound row %s: %w", id, err)
			}
			raw, err := tx.OpenRow("gateway_inbound.payload", platform+":"+updateID, sealed, payload)
			if err != nil {
				return fmt.Errorf("pending inbound payload %s: %w", id, err)
			}
			p.Platform, p.UpdateID, p.Payload = platform, updateID, raw
			var content struct {
				Text string `json:"text"`
			}
			if json.Unmarshal([]byte(raw), &content) == nil {
				p.Text = content.Text
			}
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("gwstore: pending inbound: %w", err)
	}
	return out, nil
}
