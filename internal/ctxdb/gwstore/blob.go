package gwstore

import (
	"context"
	"fmt"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// BlobOwned reports whether sha is readable by the gateway principal
// "<platform>:<identity>" (ADR-084): it must be referenced by a session whose
// newest gateway_chat label is that principal, or by an inbound update that
// identity sent. Another identity's reference is denied.
func (s Store) BlobOwned(ctx context.Context, sha, platform, identity string) (bool, error) {
	principal := platform + ":" + identity
	var owned int
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT CASE WHEN
			EXISTS (SELECT 1 FROM blob_refs r WHERE r.sha256 = ?1 AND EXISTS (
				SELECT 1 FROM session_labels l
				WHERE l.session_id = r.session_id AND l.key = 'gateway_chat' AND l.value = ?2
				  AND l.id = (SELECT max(l2.id) FROM session_labels l2
				              WHERE l2.session_id = l.session_id AND l2.key = 'gateway_chat')))
			OR EXISTS (SELECT 1 FROM blob_refs r JOIN gateway_inbound i
			  ON r.owner_kind = 'gateway_inbound' AND r.owner_id = i.platform || ':' || i.external_id
			  WHERE r.sha256 = ?1 AND i.platform = ?3 AND i.sender_id = ?4)
			THEN 1 ELSE 0 END`, []any{sha, principal, platform, identity}, &owned)
	})
	if err != nil {
		return false, fmt.Errorf("gwstore blob owned: %w", err)
	}
	return owned == 1, nil
}
