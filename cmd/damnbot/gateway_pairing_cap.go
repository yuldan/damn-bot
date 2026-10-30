package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	configos "github.com/damnbot/damnbot/internal/config"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/gwstore"
	"github.com/damnbot/damnbot/internal/gateway"
)

// SEC-27 pending pairing bounds: at most one unused code per source, ten per
// platform with oldest-first eviction, and one owner notice per ten minutes
// when the platform is full.
const (
	pairCapSource   = 1
	pairCapPlatform = 10
	pairNoticeEvery = 10 * time.Minute
	pairCapModule   = "gateway"
)

type pairPending struct {
	key, hash string
	at        int64
}

func pairIndexPrefix(platform string) string { return "pairsrc:" + platform + ":" }

func pairCodeKey(platform, hash string) string { return "pair:" + platform + ":" + hash }

// prunePairingPending enforces the SEC-27 caps before a new code is issued.
// The returned notice is true at most once per pairNoticeEvery.
func prunePairingPending(ctx context.Context, store gwstore.Store, platform, source string, now time.Time) (evicted int, notice bool, err error) {
	err = store.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		pending, xerr := loadPairPending(tx, platform)
		if xerr != nil {
			return xerr
		}
		full := len(pending) >= pairCapPlatform
		evicted, _, xerr = evictPairPending(tx, platform, source, pending)
		if xerr != nil {
			return xerr
		}
		notice, xerr = pairCapNotice(tx, platform, now, full)
		return xerr
	})
	if err != nil {
		return 0, false, fmt.Errorf("gateway: pairing cap: %w", err)
	}
	return evicted, notice, nil
}

// loadPairPending reads the index rows of one platform.
func loadPairPending(tx *ctxdb.Tx, platform string) ([]pairPending, error) {
	keys, err := tx.Strings(`SELECT key FROM module_state WHERE module_id = ?1 AND key LIKE ?2`,
		pairCapModule, pairIndexPrefix(platform)+"%")
	if err != nil {
		return nil, fmt.Errorf("pairing index: %w", err)
	}
	var pending []pairPending
	for _, key := range keys {
		var raw string
		if err := tx.Get(`SELECT value FROM module_state WHERE module_id = ?1 AND key = ?2`,
			[]any{pairCapModule, key}, &raw); err != nil {
			return nil, fmt.Errorf("pairing row %s: %w", key, err)
		}
		var meta struct {
			Hash string `json:"hash"`
			At   int64  `json:"at"`
		}
		if json.Unmarshal([]byte(raw), &meta) == nil && meta.Hash != "" {
			pending = append(pending, pairPending{key: key, hash: meta.Hash, at: meta.At})
		}
	}
	return pending, nil
}

// evictPairPending removes the oldest code until both caps hold. A per-source
// violation evicts that source's oldest code; a platform violation evicts the
// globally oldest one.
func evictPairPending(tx *ctxdb.Tx, platform, source string, pending []pairPending) (int, []pairPending, error) {
	needle := pairIndexPrefix(platform) + source + ":"
	evicted := 0
	for len(pending) >= pairCapPlatform || sourcePending(pending, platform, source) >= pairCapSource {
		victim := oldestPair(pending, needle, sourcePending(pending, platform, source) >= pairCapSource)
		if victim < 0 {
			break
		}
		p := pending[victim]
		if _, err := tx.Exec(`DELETE FROM module_state WHERE module_id = ?1 AND key IN (?2, ?3)`,
			pairCapModule, p.key, pairCodeKey(platform, p.hash)); err != nil {
			return 0, nil, fmt.Errorf("evict pairing %s: %w", p.key, err)
		}
		pending = append(pending[:victim], pending[victim+1:]...)
		evicted++
	}
	return evicted, pending, nil
}

func oldestPair(pending []pairPending, needle string, perSource bool) int {
	oldest := -1
	for i := range pending {
		if perSource && !strings.HasPrefix(pending[i].key, needle) {
			continue
		}
		if oldest < 0 || pending[i].at < pending[oldest].at {
			oldest = i
		}
	}
	return oldest
}

// sourcePending counts pending codes of one source.
func sourcePending(pending []pairPending, platform, source string) int {
	needle := pairIndexPrefix(platform) + source + ":"
	n := 0
	for _, p := range pending {
		if strings.HasPrefix(p.key, needle) {
			n++
		}
	}
	return n
}

// pairCapNotice returns true at most once per pairNoticeEvery while the
// platform table was full at the start of this prune.
func pairCapNotice(tx *ctxdb.Tx, platform string, now time.Time, full bool) (bool, error) {
	if !full {
		return false, nil
	}
	var last int64
	if err := tx.Get(`SELECT updated_at FROM module_state WHERE module_id = ?1 AND key = ?2`,
		[]any{pairCapModule, "pairnotice:" + platform}, &last); err != nil && !isNotFound(err) {
		return false, fmt.Errorf("pairing notice clock: %w", err)
	}
	if now.UnixMilli()-last < pairNoticeEvery.Milliseconds() {
		return false, nil
	}
	if _, err := tx.Exec(`INSERT INTO module_state(module_id, key, value, updated_at) VALUES (?1, ?2, 'full', ?3)
		ON CONFLICT(module_id, key) DO UPDATE SET updated_at = excluded.updated_at`,
		pairCapModule, "pairnotice:"+platform, now.UnixMilli()); err != nil {
		return false, fmt.Errorf("pairing notice: %w", err)
	}
	return true, nil
}

// recordPairingPending indexes a freshly issued code for the caps above. The
// plaintext code is never stored: only its sha256, exactly like gwstore.
func recordPairingPending(ctx context.Context, store gwstore.Store, platform, source, code string, now time.Time) error {
	sum := sha256.Sum256([]byte(code))
	hash := hex.EncodeToString(sum[:])
	value, err := json.Marshal(map[string]any{"hash": hash, "at": now.UnixMilli()})
	if err != nil {
		return fmt.Errorf("gateway: pairing index encode: %w", err)
	}
	key := fmt.Sprintf("%s%s:%s", pairIndexPrefix(platform), source, hash)
	err = store.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		if _, xerr := tx.Exec(`INSERT INTO module_state(module_id, key, value, updated_at) VALUES (?1, ?2, ?3, ?4)
			ON CONFLICT(module_id, key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			pairCapModule, key, string(value), now.UnixMilli()); xerr != nil {
			return fmt.Errorf("pairing index write: %w", xerr)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("gateway: pairing index: %w", err)
	}
	return nil
}

// notifyPairingOwner enqueues one owner notice when the pending platform cap is
// full. It is rate-limited by prunePairingPending and keyed to the same window.
func notifyPairingOwner(ctx context.Context, store gwstore.Store, platform string, now time.Time) {
	owner := strings.TrimSpace(configos.OS().Getenv("DAMNBOT_GATEWAY_OWNER"))
	p, chat, ok := strings.Cut(owner, ":")
	if !ok || p != platform {
		return
	}
	key := fmt.Sprintf("sys:pending-pairing-full:%s:%d", platform, now.Truncate(pairNoticeEvery).Unix())
	if _, _, err := store.EnqueueDelivery(ctx, &gwstore.Delivery{
		IdemKey: gateway.DeliveryKey(platform, chat, key), Platform: platform, ChatID: chat,
		Body: fmt.Sprintf("pending pairing %s penuh (%d); kode tertua dihapus, user sah tetap bisa mendapat kode", platform, pairCapPlatform),
	}); err != nil {
		return
	}
}
