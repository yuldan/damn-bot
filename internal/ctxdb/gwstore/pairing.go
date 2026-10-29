package gwstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// Pairing errors.
var (
	ErrInvalidPairing = errors.New("gwstore: invalid pairing code")
	ErrPairingUsed    = errors.New("gwstore: pairing code already used")
	ErrPairingExpired = errors.New("gwstore: pairing code expired")
	ErrPairingQuota   = errors.New("gwstore: pairing rate limit exceeded")
)

const (
	pairingModule = "gateway"
	pairingHourMs = int64(time.Hour / time.Millisecond)
	pairingDigits = 6
)

type pairingState struct {
	Until int64  `json:"until"`
	Role  string `json:"role"`
	Used  bool   `json:"used"`
}

type quotaState struct {
	At []int64 `json:"at"`
}

// Pairing is one freshly created code. The plaintext code is shown once; only
// its sha256 is stored.
type Pairing struct {
	Code      string
	Role      string
	ExpiresAt int64
}

// CreatePairing issues a six-digit single-use code for platform, valid for ttl.
// At most limit codes per source per hour (SPEC §9.7, SEC-27). Pairing requires
// an allowlisted owner; when gateway_identities is empty the first code grants
// the owner role (bootstrap).
func (s Store) CreatePairing(ctx context.Context, platform, source string, now int64, ttl time.Duration, limit int) (Pairing, error) {
	if !validPlatform(platform) || source == "" || strings.ContainsAny(source, "\x00\r\n") {
		return Pairing{}, fmt.Errorf("%w: %s/%s", ErrInvalidIdentity, platform, source)
	}
	if ttl <= 0 || limit <= 0 {
		return Pairing{}, fmt.Errorf("%w: ttl and limit must be positive", ErrInvalidPairing)
	}
	var out Pairing
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		role, rerr := pairingRole(tx)
		if rerr != nil {
			return rerr
		}
		window, werr := pairingWindow(tx, platform, source, now, limit)
		if werr != nil {
			return werr
		}
		code, cerr := issueCode(tx, platform, role, now, ttl)
		if cerr != nil {
			return cerr
		}
		if serr := saveQuota(tx, platform, source, window, now); serr != nil {
			return serr
		}
		out = Pairing{Code: code, Role: role, ExpiresAt: now + ttl.Milliseconds()}
		return nil
	})
	if err != nil {
		return Pairing{}, fmt.Errorf("gwstore pairing: %w", err)
	}
	return out, nil
}

// pairingRole refuses to issue codes without an allowlisted owner, except for
// the bootstrap case (no identities at all), which grants owner.
func pairingRole(tx *ctxdb.Tx) (string, error) {
	counts, err := storeCounts(tx)
	if err != nil {
		return "", err
	}
	if counts.Total > 0 && counts.Owners == 0 {
		return "", ErrOwnerRequired
	}
	if counts.Total == 0 {
		return "owner", nil
	}
	return "user", nil
}

// pairingWindow prunes the per-source hour window and returns it with now.
func pairingWindow(tx *ctxdb.Tx, platform, source string, now int64, limit int) ([]int64, error) {
	var raw string
	err := tx.Get(`SELECT value FROM module_state WHERE module_id = ?1 AND key = ?2`,
		[]any{pairingModule, "pairq:" + platform + ":" + source}, &raw)
	if err != nil && !errors.Is(err, ctxdb.ErrNotFound) {
		return nil, fmt.Errorf("pairing quota: %w", err)
	}
	var at []int64
	if err == nil {
		var quota quotaState
		if jerr := json.Unmarshal([]byte(raw), &quota); jerr != nil {
			return nil, fmt.Errorf("%w: quota state", ErrInvalidPairing)
		}
		at = quota.At
	}
	return pruneQuota(at, now, limit)
}

func pruneQuota(at []int64, now int64, limit int) ([]int64, error) {
	window := make([]int64, 0, len(at)+1)
	for _, v := range at {
		if now-v < pairingHourMs {
			window = append(window, v)
		}
	}
	if len(window) >= limit {
		return nil, ErrPairingQuota
	}
	return append(window, now), nil
}

func saveQuota(tx *ctxdb.Tx, platform, source string, window []int64, now int64) error {
	j, err := json.Marshal(quotaState{At: window})
	if err != nil {
		return fmt.Errorf("quota encode: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO module_state(module_id, key, value, updated_at) VALUES (?1, ?2, ?3, ?4)
		ON CONFLICT(module_id, key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		pairingModule, "pairq:"+platform+":"+source, string(j), now); err != nil {
		return fmt.Errorf("pairing quota: %w", err)
	}
	return nil
}

// issueCode inserts one unused code row and returns the plaintext.
func issueCode(tx *ctxdb.Tx, platform, role string, now int64, ttl time.Duration) (string, error) {
	until := now + ttl.Milliseconds()
	value, err := json.Marshal(pairingState{Until: until, Role: role})
	if err != nil {
		return "", fmt.Errorf("pairing encode: %w", err)
	}
	for range 5 {
		code, err := randomDigits()
		if err != nil {
			return "", err
		}
		n, err := tx.Exec(`INSERT INTO module_state(module_id, key, value, updated_at) VALUES (?1, ?2, ?3, ?4)
			ON CONFLICT(module_id, key) DO NOTHING`,
			pairingModule, pairingKey(platform, code), string(value), now)
		if err != nil {
			return "", fmt.Errorf("pairing insert: %w", err)
		}
		if n == 1 {
			return code, nil
		}
	}
	return "", fmt.Errorf("%w: code collision", ErrInvalidPairing)
}

func randomDigits() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", fmt.Errorf("pairing random: %w", err)
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

func pairingKey(platform, code string) string {
	sum := sha256.Sum256([]byte(code))
	return "pair:" + platform + ":" + hex.EncodeToString(sum[:])
}

// RedeemPairing consumes code for (platform, externalID) and allowlists that
// identity. A second redemption is ErrPairingUsed; an expired code is
// ErrPairingExpired; a weak identity (SEC-10) or a blocked row is refused.
func (s Store) RedeemPairing(ctx context.Context, platform, code, externalID, strength string, now int64) (Identity, error) {
	if !validPlatform(platform) || !ValidIdentity(externalID) || len(code) != pairingDigits || !digits(code) {
		return Identity{}, fmt.Errorf("%w: %s/%s", ErrInvalidIdentity, platform, externalID)
	}
	if !validStrength(strength) || strength == "weak" {
		return Identity{}, fmt.Errorf("%w: strength %q", ErrInvalidIdentity, strength)
	}
	var out Identity
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		st, key, rerr := readPairing(tx, platform, code, now)
		if rerr != nil {
			return rerr
		}
		if cerr := checkRedeemTarget(tx, platform, externalID); cerr != nil {
			return cerr
		}
		if aerr := allowlist(tx, platform, externalID, st.Role, strength, now); aerr != nil {
			return aerr
		}
		if uerr := consumePairing(tx, key, st, now); uerr != nil {
			return uerr
		}
		out = Identity{Role: st.Role, Status: "allowlisted", Strength: strength}
		return nil
	})
	if err != nil {
		return Identity{}, fmt.Errorf("gwstore pairing: %w", err)
	}
	return out, nil
}

func readPairing(tx *ctxdb.Tx, platform, code string, now int64) (pairingState, string, error) {
	key := pairingKey(platform, code)
	var raw string
	if err := tx.Get(`SELECT value FROM module_state WHERE module_id = ?1 AND key = ?2`, []any{pairingModule, key}, &raw); err != nil {
		if errors.Is(err, ctxdb.ErrNotFound) {
			return pairingState{}, "", ErrInvalidPairing
		}
		return pairingState{}, "", fmt.Errorf("pairing read: %w", err)
	}
	var st pairingState
	if err := json.Unmarshal([]byte(raw), &st); err != nil || !validRole(st.Role) {
		return pairingState{}, "", fmt.Errorf("%w: state", ErrInvalidPairing)
	}
	switch {
	case st.Used:
		return pairingState{}, "", ErrPairingUsed
	case now >= st.Until:
		return pairingState{}, "", ErrPairingExpired
	}
	return st, key, nil
}

// checkRedeemTarget refuses to promote a blocked or weak stored identity.
func checkRedeemTarget(tx *ctxdb.Tx, platform, externalID string) error {
	var role, status, strength string
	err := tx.Get(`SELECT role, status, strength FROM gateway_identities WHERE platform = ?1 AND external_id = ?2`,
		[]any{platform, externalID}, &role, &status, &strength)
	switch {
	case err == nil && status == "blocked":
		return ErrIdentityBlocked
	case err == nil && strength == "weak":
		return fmt.Errorf("%w: stored strength weak", ErrInvalidIdentity)
	case err != nil && !errors.Is(err, ctxdb.ErrNotFound):
		return fmt.Errorf("identity read: %w", err)
	}
	return nil
}

func allowlist(tx *ctxdb.Tx, platform, externalID, role, strength string, now int64) error {
	if _, err := tx.Exec(`INSERT INTO gateway_identities(platform, external_id, display_name, role, status, strength, updated_at)
		VALUES (?1, ?2, ?2, ?3, 'allowlisted', ?4, ?5)
		ON CONFLICT(platform, external_id) DO UPDATE SET
			role = CASE WHEN gateway_identities.role = 'owner' THEN 'owner' ELSE excluded.role END,
			status = 'allowlisted', updated_at = excluded.updated_at`,
		platform, externalID, role, strength, now); err != nil {
		return fmt.Errorf("pairing upsert: %w", err)
	}
	return nil
}

func consumePairing(tx *ctxdb.Tx, key string, st pairingState, now int64) error {
	st.Used = true
	encoded, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("pairing encode: %w", err)
	}
	if _, err := tx.Exec(`UPDATE module_state SET value = ?3, updated_at = ?4 WHERE module_id = ?1 AND key = ?2`,
		pairingModule, key, string(encoded), now); err != nil {
		return fmt.Errorf("pairing consume: %w", err)
	}
	return nil
}

func digits(s string) bool {
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
