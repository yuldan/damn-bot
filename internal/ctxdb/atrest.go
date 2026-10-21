package ctxdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Codec seals and opens field values at rest. Scope is a table.column label
// and is bound as AEAD additional data; ID identifies the database DEK.
type Codec interface {
	ID() string
	Seal(scope string, plaintext []byte) ([]byte, error)
	Open(scope string, sealed []byte) ([]byte, error)
	Hash(data string) string
}

func (s *Store) checkAtRest(ctx context.Context) error {
	var id string
	err := s.r.QueryRowContext(ctx, `SELECT value FROM module_state WHERE module_id = 'storage' AND key = 'encrypt'`).Scan(&id)
	switch {
	case err == nil:
		if s.codec == nil {
			return fmt.Errorf("%w: storage.encrypt is false", ErrEncrypted)
		}
		if id != s.codec.ID() {
			return fmt.Errorf("%w: DEK id mismatch", ErrEncrypted)
		}
		return nil
	case !errors.Is(err, sql.ErrNoRows):
		return wrap(err)
	}
	if s.codec == nil {
		return nil
	}
	var n int64
	if qerr := s.r.QueryRowContext(ctx, `SELECT count(*) FROM events`).Scan(&n); qerr != nil {
		return wrap(qerr)
	}
	if n != 0 {
		return fmt.Errorf("%w: DB already contains plaintext events", ErrAtRestPlaintext)
	}
	_, err = s.w.ExecContext(ctx, `INSERT INTO module_state(module_id, key, value, updated_at) VALUES ('storage','encrypt',?,?)`,
		s.codec.ID(), s.clk.WallMs())
	return wrap(err)
}

func (s *Store) sealRow(scope, identity, plain, placeholder string) ([]byte, string, error) {
	if s.codec == nil {
		return nil, plain, nil
	}
	sealed, err := s.codec.Seal(scope, []byte(identity+"\x00"+plain))
	if err != nil {
		return nil, "", fmt.Errorf("ctxdb: seal %s: %w", scope, err)
	}
	return sealed, placeholder, nil
}

func (s *Store) openRow(scope, identity string, sealed []byte, plain string) (string, error) {
	if len(sealed) == 0 {
		return plain, nil
	}
	if s.codec == nil {
		return "", ErrEncrypted
	}
	pt, err := s.codec.Open(scope, sealed)
	if err != nil {
		return "", fmt.Errorf("ctxdb: open %s: %w", scope, err)
	}
	prefix := identity + "\x00"
	if !strings.HasPrefix(string(pt), prefix) {
		return "", fmt.Errorf("ctxdb: row identity mismatch: %w", ErrEncrypted)
	}
	return string(pt[len(prefix):]), nil
}

// SealRow encrypts one consumer value with its row identity.
func (t *Tx) SealRow(scope, identity, plain, placeholder string) ([]byte, string, error) {
	return t.s.sealRow(scope, identity, plain, placeholder)
}

// OpenRow decrypts one consumer value and verifies its row identity.
func (t *Tx) OpenRow(scope, identity string, sealed []byte, plain string) (string, error) {
	return t.s.openRow(scope, identity, sealed, plain)
}
