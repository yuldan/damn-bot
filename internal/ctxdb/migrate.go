package ctxdb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migFS embed.FS

type migration struct {
	ver        int
	name, body string
	sum        string
}

// migrations returns the embedded ladder; numbering must be gap-free (§11.1).
func migrations() ([]migration, error) {
	ents, err := migFS.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("ctxdb: migrations: %w", err)
	}
	var out []migration
	for _, e := range ents {
		b, err := migFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("ctxdb: migrations: %w", err)
		}
		v, _ := strconv.Atoi(e.Name()[:4])
		h := sha256.Sum256(b)
		out = append(out, migration{ver: v, name: strings.TrimSuffix(e.Name(), ".sql"), body: string(b), sum: hex.EncodeToString(h[:])})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ver < out[j].ver })
	for i, m := range out {
		if m.ver != i+1 {
			return nil, fmt.Errorf("ctxdb: migration gap at %s: %w", m.name, ErrInternal)
		}
	}
	return out, nil
}

// schemaMax is the newest schema this binary understands.
func (s *Store) schemaMax() int {
	if s.opt.SchemaMax > 0 {
		return s.opt.SchemaMax
	}
	m, _ := migrations()
	return len(m)
}

// migrateIfNeeded applies pending migrations under the barrier reason=migrate
// (§11.1, §11.11.7). Old non-serve holders refuse the migration before any
// marker is written unless --force-migrate.
func (s *Store) migrateIfNeeded(ctx context.Context) error {
	migs, err := migrations()
	if err != nil {
		return err
	}
	target := s.schemaMax()
	cur, err := s.dbVersion(ctx, migs)
	if err != nil || cur == target {
		return err
	}
	if cur > target {
		return fmt.Errorf("ctxdb: db version %d > binary %d: %w", cur, target, ErrSchemaNewer)
	}
	if err := s.checkOldHolders(target); err != nil {
		return err
	}
	b, err := AcquireBarrier(ctx, s.dir, s.id, reasonMigrate, 30, s.opt.BarrierWait)
	if err != nil {
		return err
	}
	defer b.Release()
	if err := s.checkOldHolders(target); err != nil { // repeated in `draining`
		return err
	}
	w, r, err := s.openHandles()
	if err != nil {
		return err
	}
	defer func() { _ = errors.Join(w.Close(), r.Close()) }()
	if cur > 0 {
		bk := filepath.Join(s.dir, "backups", fmt.Sprintf("premigrate-%d-%d.db", cur, target))
		_ = os.Remove(bk)
		if _, err := w.ExecContext(ctx, "VACUUM INTO ?", bk); err != nil {
			return fmt.Errorf("ctxdb: premigrate backup: %w", classify(err))
		}
		pruneBackups(filepath.Join(s.dir, "backups"), "premigrate-", 2)
	}
	for _, m := range migs[cur:target] {
		if err := applyMigration(ctx, w, m, s.clk.WallMs()); err != nil {
			return err
		}
	}
	_ = os.Remove(runPath(s.dir, "migrate_blocked.json"))
	return nil
}

// dbVersion reads user_version and verifies checksums of applied migrations.
func (s *Store) dbVersion(ctx context.Context, migs []migration) (int, error) {
	if _, err := os.Stat(DBPath(s.dir)); errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	w, r, err := s.openHandles()
	if err != nil {
		return 0, err
	}
	defer func() { _ = errors.Join(w.Close(), r.Close()) }()
	var v int
	if err := r.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil || v == 0 {
		return 0, classify(err)
	}
	rows, err := r.QueryContext(ctx, "SELECT version, checksum FROM schema_migrations")
	if err != nil {
		return 0, classify(err)
	}
	defer rows.Close()
	for rows.Next() {
		var ver int
		var sum string
		if err := rows.Scan(&ver, &sum); err != nil {
			return 0, err
		}
		if ver <= len(migs) && migs[ver-1].sum != sum {
			return 0, fmt.Errorf("ctxdb: migration %d checksum mismatch: %w", ver, ErrInternal)
		}
	}
	return v, rows.Err()
}

// applyMigration runs one migration in one transaction and records it in
// schema_migrations + PRAGMA user_version (§11.1).
func applyMigration(ctx context.Context, w *sql.DB, m migration, now int64) (err error) {
	c, err := w.Conn(ctx)
	if err != nil {
		return classify(err)
	}
	defer func() { err = joinClose(err, c) }()
	if _, err := c.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return classify(err)
	}
	_, err = c.ExecContext(ctx, m.body)
	if err == nil {
		_, err = c.ExecContext(ctx, "INSERT INTO schema_migrations(version, name, checksum, applied_at) VALUES (?,?,?,?)", m.ver, m.name, m.sum, now)
	}
	if err == nil {
		_, err = c.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", m.ver))
	}
	if err == nil {
		_, err = c.ExecContext(ctx, "COMMIT")
	}
	if err != nil {
		_, _ = c.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		return fmt.Errorf("ctxdb: migration %s: %w", m.name, classify(err))
	}
	return nil
}

// checkOldHolders refuses a migration while a live non-serve holder only
// understands an older schema (checked before the marker, §11.11.7). A serve
// migrator that keeps being refused forces after 10 min of accumulated
// boot-clock time persisted across restarts in run/migrate_blocked.json (AF-08).
func (s *Store) checkOldHolders(target int) error {
	var old []string
	for _, h := range s.holders() {
		if h.Kind != "serve" && h.SchemaMax < target {
			old = append(old, fmt.Sprintf("%s pid %d", h.Kind, h.PID))
		}
	}
	if len(old) == 0 || s.opt.ForceMigrate || (s.opt.Kind == "serve" && s.migrateBlockedLong(target)) {
		return nil
	}
	return fmt.Errorf("ctxdb: old processes running (%s): %w", strings.Join(old, ", "), ErrMigrationRefused)
}

type migrateBlocked struct {
	SinceBoot int64  `json:"since_boot"`
	BootID    string `json:"boot_id"`
	Target    int    `json:"target_version"`
}

// migrateBlockedLong records the first refusal (boot clock) and reports
// whether ≥ 600 s have elapsed since, resetting on a new boot or target.
func (s *Store) migrateBlockedLong(target int) bool {
	p := runPath(s.dir, "migrate_blocked.json")
	var mb migrateBlocked
	b, err := os.ReadFile(p)
	if err != nil || json.Unmarshal(b, &mb) != nil || mb.BootID != s.id.BootID || mb.Target != target {
		mb = migrateBlocked{SinceBoot: s.clk.BootMs(), BootID: s.id.BootID, Target: target}
		nb, _ := json.Marshal(mb)
		_ = writeAtomic(p, nb)
	}
	return s.clk.BootMs()-mb.SinceBoot >= 600_000
}

// pruneBackups keeps the newest `keep` files with the given prefix.
func pruneBackups(dir, prefix string, keep int) {
	ents, _ := os.ReadDir(dir)
	var fs []os.FileInfo
	for _, e := range ents {
		if fi, err := e.Info(); err == nil && strings.HasPrefix(e.Name(), prefix) {
			fs = append(fs, fi)
		}
	}
	sort.Slice(fs, func(i, j int) bool { return fs[i].ModTime().After(fs[j].ModTime()) })
	for i := keep; i < len(fs); i++ {
		_ = os.Remove(filepath.Join(dir, fs[i].Name()))
	}
}
