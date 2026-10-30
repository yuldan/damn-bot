package gwstore

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// Identity administration errors.
var (
	ErrInvalidIdentity = errors.New("gwstore: invalid identity")
	ErrIdentityBlocked = errors.New("gwstore: identity is blocked")
	ErrOwnerRequired   = errors.New("gwstore: an allowlisted owner is required")
)

// StoredIdentity is one gateway_identities row with its key.
type StoredIdentity struct {
	Platform    string
	ExternalID  string
	DisplayName string
	Role        string
	Status      string
	Strength    string
	Workspace   string
}

// IdentityCounts summarises gateway_identities for `gateway status`.
type IdentityCounts struct {
	Total       int
	Owners      int
	Allowlisted int
	Blocked     int
	Pending     int
}

// ValidIdentity reports whether id is a SPEC §12 identity: 1..128 chars from
// [A-Za-z0-9_.:@-], starting alphanumeric.
func ValidIdentity(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for i := range len(id) {
		c := id[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case i > 0 && (c == '_' || c == '.' || c == ':' || c == '@' || c == '-'):
		default:
			return false
		}
	}
	return true
}

func validPlatform(p string) bool { return ValidIdentity(p) }
func validRole(r string) bool     { return r == "owner" || r == "user" }
func validStatus(s string) bool   { return s == "allowlisted" || s == "blocked" }
func validStrength(s string) bool {
	return s == "cryptographic" || s == "platform_authenticated" || s == "weak"
}

// SetIdentity writes status and role idempotently: the row is created (strength
// platform_authenticated, the operator asserted it) or updated in place. A
// blocked identity stays blocked until an explicit allow.
func (s Store) SetIdentity(ctx context.Context, platform, externalID, role, status string, now int64) error {
	if !validPlatform(platform) || !ValidIdentity(externalID) {
		return fmt.Errorf("%w: %s:%s", ErrInvalidIdentity, platform, externalID)
	}
	if !validRole(role) {
		return fmt.Errorf("%w: role %q", ErrInvalidIdentity, role)
	}
	if !validStatus(status) {
		return fmt.Errorf("%w: status %q", ErrInvalidIdentity, status)
	}
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		_, xerr := tx.Exec(`INSERT INTO gateway_identities(platform, external_id, display_name, role, status, strength, updated_at)
			VALUES (?1, ?2, ?2, ?3, ?4, 'platform_authenticated', ?5)
			ON CONFLICT(platform, external_id) DO UPDATE SET role = excluded.role, status = excluded.status, updated_at = excluded.updated_at`,
			platform, externalID, role, status, now)
		if xerr != nil {
			return fmt.Errorf("set identity: %w", xerr)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("gwstore set identity: %w", err)
	}
	return nil
}

// SetStatus changes only the status (verify/revoke), preserving the role. A
// missing row is created as a blocked user, which is fail-closed.
func (s Store) SetStatus(ctx context.Context, platform, externalID, status string, now int64) error {
	if !validPlatform(platform) || !ValidIdentity(externalID) {
		return fmt.Errorf("%w: %s:%s", ErrInvalidIdentity, platform, externalID)
	}
	if !validStatus(status) {
		return fmt.Errorf("%w: status %q", ErrInvalidIdentity, status)
	}
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		_, xerr := tx.Exec(`INSERT INTO gateway_identities(platform, external_id, display_name, role, status, strength, updated_at)
			VALUES (?1, ?2, ?2, 'user', ?3, 'platform_authenticated', ?4)
			ON CONFLICT(platform, external_id) DO UPDATE SET status = excluded.status, updated_at = excluded.updated_at`,
			platform, externalID, status, now)
		if xerr != nil {
			return fmt.Errorf("set status: %w", xerr)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("gwstore set status: %w", err)
	}
	return nil
}

// EnsureIdentity bootstraps a row from the environment and never overwrites an
// existing one: the database stays the source of truth. inserted reports a new
// row.
func (s Store) EnsureIdentity(ctx context.Context, platform, externalID, role, strength string, now int64) (bool, error) {
	if !validPlatform(platform) || !ValidIdentity(externalID) || !validRole(role) || !validStrength(strength) {
		return false, fmt.Errorf("%w: %s:%s", ErrInvalidIdentity, platform, externalID)
	}
	inserted := false
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		ids, xerr := tx.Strings(`INSERT INTO gateway_identities(platform, external_id, display_name, role, status, strength, updated_at)
			VALUES (?1, ?2, ?2, ?3, 'allowlisted', ?4, ?5)
			ON CONFLICT(platform, external_id) DO NOTHING
			RETURNING external_id`, platform, externalID, role, strength, now)
		if xerr != nil {
			return fmt.Errorf("ensure identity: %w", xerr)
		}
		inserted = len(ids) == 1
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("gwstore ensure identity: %w", err)
	}
	return inserted, nil
}

// ListIdentities returns every identity, sorted by platform then id.
func (s Store) ListIdentities(ctx context.Context) ([]StoredIdentity, error) {
	var out []StoredIdentity
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		rows, xerr := tx.Strings(`SELECT platform || char(31) || external_id || char(31) || display_name || char(31)
				|| role || char(31) || status || char(31) || strength || char(31) || coalesce(workspace, '')
			FROM gateway_identities ORDER BY platform, external_id`)
		if xerr != nil {
			return fmt.Errorf("list identities: %w", xerr)
		}
		out = out[:0]
		for _, row := range rows {
			f := strings.Split(row, "\x1f")
			if len(f) != 7 {
				return fmt.Errorf("list identities: malformed row")
			}
			out = append(out, StoredIdentity{
				Platform: f[0], ExternalID: f[1], DisplayName: f[2],
				Role: f[3], Status: f[4], Strength: f[5], Workspace: f[6],
			})
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].Platform != out[j].Platform {
				return out[i].Platform < out[j].Platform
			}
			return out[i].ExternalID < out[j].ExternalID
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("gwstore list identities: %w", err)
	}
	return out, nil
}

// Counts returns the identity counts of `gateway status`.
func (s Store) Counts(ctx context.Context) (IdentityCounts, error) {
	var c IdentityCounts
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		var cerr error
		c, cerr = storeCounts(tx)
		return cerr
	})
	if err != nil {
		return IdentityCounts{}, fmt.Errorf("gwstore counts: %w", err)
	}
	return c, nil
}

// Status is the read-only summary of `damnbot gateway status`.
type Status struct {
	Identities IdentityCounts
	Inbound    int
	Deliveries map[string]int
	Leases     int
}

// Status reads identity, inbound queue, outbox and lease counts. Nothing is
// written.
func (s Store) Status(ctx context.Context) (Status, error) {
	var st Status
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		var err error
		if st.Identities, err = storeCounts(tx); err != nil {
			return err
		}
		var n int
		if err := tx.Get(`SELECT count(*) FROM gateway_inbound WHERE event_id IS NULL`, nil, &n); err != nil {
			return fmt.Errorf("inbound queue: %w", err)
		}
		st.Inbound = n
		st.Deliveries = map[string]int{}
		rows, xerr := tx.Strings(`SELECT status || char(31) || count(*) FROM gateway_deliveries GROUP BY status`)
		if xerr != nil {
			return fmt.Errorf("deliveries: %w", xerr)
		}
		for _, row := range rows {
			status, countText, ok := strings.Cut(row, "\x1f")
			if !ok {
				return fmt.Errorf("deliveries row: malformed")
			}
			count, cerr := strconv.Atoi(countText)
			if cerr != nil {
				return fmt.Errorf("deliveries row: %w", cerr)
			}
			st.Deliveries[status] = count
		}
		if err := tx.Get(`SELECT count(*) FROM session_leases`, nil, &n); err != nil {
			return fmt.Errorf("leases: %w", err)
		}
		st.Leases = n
		return nil
	})
	if err != nil {
		return Status{}, fmt.Errorf("gwstore status: %w", err)
	}
	return st, nil
}

func storeCounts(tx *ctxdb.Tx) (IdentityCounts, error) {
	var c IdentityCounts
	err := tx.Get(`SELECT count(*),
			coalesce(sum(role = 'owner' AND status = 'allowlisted'), 0),
			coalesce(sum(status = 'allowlisted'), 0),
			coalesce(sum(status = 'blocked'), 0),
			coalesce(sum(status = 'pending'), 0)
		FROM gateway_identities`, nil,
		&c.Total, &c.Owners, &c.Allowlisted, &c.Blocked, &c.Pending)
	if err != nil {
		return IdentityCounts{}, fmt.Errorf("identity counts: %w", err)
	}
	return c, nil
}

// ChatSetting reads one per-chat override.
func (s Store) ChatSetting(ctx context.Context, platform, chatID, name string) (value string, found bool, err error) {
	err = s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT value FROM module_state WHERE module_id = 'gateway' AND key = ?1`,
			[]any{"chats." + platform + ":" + chatID + "." + name}, &value)
	})
	if err != nil && !errors.Is(err, ctxdb.ErrNotFound) {
		return "", false, fmt.Errorf("gwstore chat setting: %w", err)
	}
	return value, value != "", nil
}

// SetChatSetting writes one per-chat override.
func (s Store) SetChatSetting(ctx context.Context, platform, chatID, name, value string, now int64) error {
	if err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		_, xerr := tx.Exec(`INSERT INTO module_state(module_id, key, value, updated_at) VALUES ('gateway', ?1, ?2, ?3)
			ON CONFLICT(module_id, key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			"chats."+platform+":"+chatID+"."+name, value, now)
		if xerr != nil {
			return fmt.Errorf("write chat setting: %w", xerr)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("gwstore set chat setting: %w", err)
	}
	return nil
}

// HomeChannel returns the /sethome destination (GW-005).
func (s Store) HomeChannel(ctx context.Context) (platform, chatID, threadID string, found bool, err error) {
	var raw string
	err = s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT value FROM module_state WHERE module_id = 'gateway' AND key = 'home_channel'`, nil, &raw)
	})
	if errors.Is(err, ctxdb.ErrNotFound) {
		return "", "", "", false, nil
	}
	if err != nil {
		return "", "", "", false, fmt.Errorf("gwstore home channel: %w", err)
	}
	parts := strings.SplitN(raw, "\x1f", 3)
	if len(parts) != 3 || !validChannel(parts[0], parts[1], parts[2]) {
		return "", "", "", false, fmt.Errorf("gwstore home channel: malformed value")
	}
	return parts[0], parts[1], parts[2], true, nil
}

// SetHomeChannel stores the idempotent GW-005 home channel.
func (s Store) SetHomeChannel(ctx context.Context, platform, chatID, threadID string, now int64) error {
	if !validChannel(platform, chatID, threadID) {
		return fmt.Errorf("gwstore home channel: invalid channel")
	}
	value := strings.Join([]string{platform, chatID, threadID}, "\x1f")
	if err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		_, xerr := tx.Exec(`INSERT INTO module_state(module_id, key, value, updated_at) VALUES ('gateway', 'home_channel', ?1, ?2)
			ON CONFLICT(module_id, key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			value, now)
		if xerr != nil {
			return fmt.Errorf("write home channel: %w", xerr)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("gwstore set home channel: %w", err)
	}
	return nil
}

func validChannel(platform, chatID, threadID string) bool {
	joined := platform + chatID + threadID
	return platform != "" && chatID != "" && len(joined) <= 1536 && !strings.ContainsAny(joined, "\x00\r\n\x1f")
}
