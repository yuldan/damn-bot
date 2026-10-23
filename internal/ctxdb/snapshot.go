package ctxdb

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// SnapshotTo writes a consistent VACUUM INTO snapshot of dir's database to dst
// and returns the sha256 of every live blob row. It is the read side used by
// encrypted backup archives (ADR-124); it does not copy blobs.
func SnapshotTo(ctx context.Context, dir, dst string, codec Codec) (shas []string, err error) {
	if _, serr := os.Stat(DBPath(dir)); serr != nil {
		if errors.Is(serr, fs.ErrNotExist) {
			return nil, fmt.Errorf("ctxdb: snapshot: %w", fs.ErrNotExist)
		}
		return nil, fmt.Errorf("ctxdb: snapshot stat: %w", serr)
	}
	s, err := Open(ctx, &Options{Dir: dir, Kind: "cli", Codec: codec})
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, s.CloseContext(ctx)) }()
	if _, err := s.w.ExecContext(ctx, "VACUUM INTO ?", dst); err != nil {
		return nil, fmt.Errorf("ctxdb: vacuum into: %w", wrap(err))
	}
	if err := os.Chmod(dst, 0o600); err != nil {
		return nil, fmt.Errorf("ctxdb: snapshot chmod: %w", err)
	}
	return s.liveBlobShas(ctx)
}

func (s *Store) liveBlobShas(ctx context.Context) (out []string, err error) {
	rows, err := s.r.QueryContext(ctx, `SELECT b.sha256 FROM blobs b
		WHERE coalesce((SELECT kind FROM blob_expirations e
			WHERE e.sha256 = b.sha256 ORDER BY e.id DESC LIMIT 1), '') <> 'expired'`)
	if err != nil {
		return nil, fmt.Errorf("ctxdb: live blobs: %w", wrap(err))
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var sha string
		if err := rows.Scan(&sha); err != nil {
			return nil, fmt.Errorf("ctxdb: live blobs scan: %w", wrap(err))
		}
		out = append(out, sha)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ctxdb: live blobs rows: %w", wrap(err))
	}
	return out, nil
}
