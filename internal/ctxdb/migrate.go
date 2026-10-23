package ctxdb

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql queries.sql
var migFS embed.FS

type migration struct {
	name, body, sum string
}

// migrations returns the embedded ladder (file names sort by number); the
// numbering must be gap-free (§11.1).
func migrations() ([]migration, error) {
	ents, err := migFS.ReadDir("migrations")
	out := make([]migration, 0, len(ents))
	for i := 0; err == nil && i < len(ents); i++ {
		name := ents[i].Name()
		var b []byte
		if b, err = migFS.ReadFile("migrations/" + name); err == nil && !strings.HasPrefix(name, fmt.Sprintf("%04d_", i+1)) {
			err = fmt.Errorf("ctxdb: migration gap at %s: %w", name, ErrInternal)
		}
		h := sha256.Sum256(b)
		out = append(out, migration{name: strings.TrimSuffix(name, ".sql"), body: string(b), sum: hex.EncodeToString(h[:])})
	}
	return out, wrap(err)
}

// loadQueries parses queries.sql into named blocks ("-- name: <id>").
func loadQueries() (map[string]string, error) {
	b, err := migFS.ReadFile("queries.sql")
	if err != nil {
		return nil, wrap(err)
	}
	out := map[string]string{}
	for _, blk := range strings.Split(string(b), "-- name: ")[1:] {
		name, body, _ := strings.Cut(blk, "\n")
		out[strings.TrimSpace(name)] = body
	}
	for k, v := range out {
		out[k] = strings.ReplaceAll(v, "/*LIVE_REF*/", out["gc.live_ref"])
	}
	return out, nil
}

// schemaMax is the newest schema this binary understands.
func (s *Store) schemaMax() int {
	if s.opt.SchemaMax > 0 {
		return s.opt.SchemaMax
	}
	return len(s.migs)
}

// migrateIfNeeded applies pending migrations under the barrier reason=migrate
// (§11.1, §11.11.7). Old non-serve holders refuse the migration before any
// marker is written unless --force-migrate.
func (s *Store) migrateIfNeeded(ctx context.Context) (err error) {
	migs, target := s.migs, s.schemaMax()
	cur, err := s.dbVersion(ctx, migs)
	switch {
	case err != nil || cur == target:
		return err
	case cur > target:
		return fmt.Errorf("ctxdb: db version %d > binary %d: %w", cur, target, ErrSchemaNewer)
	}
	if herr := s.checkOldHolders(target); herr != nil {
		return herr
	}
	b, err := s.acquireMigrationBarrier(ctx, migs, target)
	if err != nil {
		return err
	}
	if b == nil { // another process completed the ladder while we waited
		return nil
	}
	defer func() { err = errors.Join(err, b.Release()) }()
	// The winner of the barrier race may have migrated while we waited: refresh
	// the version before applying anything (EC-STATE-003).
	if v, verr := s.dbVersion(ctx, migs); verr != nil {
		return verr
	} else if v >= target {
		return nil
	}
	if herr := s.checkOldHolders(target); herr != nil { // repeated in `draining`
		return herr
	}
	if s.w, s.r, err = s.openHandles(ctx); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.detach()) }()
	if cur > 0 {
		if berr := s.backupPremigrate(ctx, cur, target); berr != nil {
			return berr
		}
	}
	for i := cur; i < target && err == nil; i++ {
		err = s.applyMigration(ctx, i+1, migs[i])
	}
	return errors.Join(err, removeIfExists(s.root, "run/migrate_blocked.json"))
}

// acquireMigrationBarrier waits out a concurrent schema initialisation
// (EC-STATE-003): another process holding the migration barrier is not a
// user-visible failure. The wait is bounded by BarrierWait and checks the
// version between attempts, so the loser of the race succeeds once the
// winner has committed.
func (s *Store) acquireMigrationBarrier(ctx context.Context, migs []migration, target int) (*Barrier, error) {
	const retry = 50 * time.Millisecond
	deadline := time.Now().Add(s.opt.BarrierWait)
	b, err := AcquireBarrier(ctx, s.dir, s.id, reasonMigrate, 30, s.opt.BarrierWait)
	// macOS can surface a transient ENOENT while the barrier owner file is
	// created/released concurrently; retry it like db_busy.
	for (errors.Is(err, ErrDBBusy) || errors.Is(err, fs.ErrNotExist)) && time.Now().Before(deadline) {
		if v, verr := s.dbVersion(ctx, migs); verr == nil && v == target {
			return nil, nil // the winner finished the ladder
		}
		if serr := sleepReal(ctx, retry); serr != nil {
			return nil, serr
		}
		b, err = AcquireBarrier(ctx, s.dir, s.id, reasonMigrate, 30, s.opt.BarrierWait)
	}
	return b, err
}

// backupPremigrate = VACUUM INTO backups/premigrate-<from>-<to>.db, keep 2.
func (s *Store) backupPremigrate(ctx context.Context, from, to int) error {
	name := fmt.Sprintf("backups/premigrate-%d-%d.db", from, to)
	err := removeIfExists(s.root, name)
	if err == nil {
		_, err = s.w.ExecContext(ctx, "VACUUM INTO ?", filepath.Join(s.dir, name))
	}
	ents, rerr := fs.ReadDir(s.root.FS(), "backups")
	var old []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "premigrate-") {
			old = append(old, e.Name())
		}
	}
	slices.Sort(old) // premigrate-<from>-<to>: older ladders sort first
	for i := 0; i+2 < len(old); i++ {
		err = errors.Join(err, removeIfExists(s.root, path.Join("backups", old[i])))
	}
	return wrap(errors.Join(err, rerr))
}

// dbVersion reads user_version and verifies checksums of applied migrations.
func (s *Store) dbVersion(ctx context.Context, migs []migration) (v int, err error) {
	if _, serr := s.root.Stat(dbFile); errors.Is(serr, fs.ErrNotExist) {
		return 0, nil
	}
	if s.w, s.r, err = s.openHandles(ctx); err != nil {
		return 0, err
	}
	defer func() { err = errors.Join(err, s.detach()) }()
	if err = getOne(ctx, s.r, "PRAGMA user_version", nil, &v); err != nil || v == 0 {
		return 0, err
	}
	if v > s.schemaMax() {
		return v, nil
	}
	sums, err := column[string](ctx, s.r, "SELECT checksum FROM schema_migrations ORDER BY version")
	for i := 0; err == nil && i < len(sums) && i < len(migs); i++ {
		if migs[i].sum != sums[i] {
			err = fmt.Errorf("ctxdb: migration %s checksum mismatch: %w", migs[i].name, ErrInternal)
		}
	}
	return v, err
}

// applyMigration runs one migration in one transaction and records it in
// schema_migrations + PRAGMA user_version (§11.1).
func (s *Store) applyMigration(ctx context.Context, ver int, m migration) (err error) {
	c, err := s.w.Conn(ctx)
	if err != nil {
		return wrap(err)
	}
	defer func() { err = errors.Join(err, wrap(c.Close())) }()
	steps := []struct {
		q    string
		args []any
	}{
		{"BEGIN IMMEDIATE", nil},
		{m.body, nil},
		{"INSERT INTO schema_migrations(version, name, checksum, applied_at) VALUES (?,?,?,?)", []any{ver, m.name, m.sum, s.clk.WallMs()}},
		{"PRAGMA user_version = " + strconv.Itoa(ver), nil},
		{"COMMIT", nil},
	}
	for i, st := range steps {
		if _, err = c.ExecContext(ctx, st.q, st.args...); err != nil {
			if i > 0 {
				_, rerr := c.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
				err = errors.Join(err, rerr)
			}
			return fmt.Errorf("ctxdb: migration %s: %w", m.name, classify(err))
		}
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
	if len(old) == 0 || s.opt.ForceMigrate || s.opt.Kind == "serve" && s.migrateBlockedLong(target) {
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
	const name = "run/migrate_blocked.json"
	var mb migrateBlocked
	b, err := s.root.ReadFile(name)
	if err != nil || json.Unmarshal(b, &mb) != nil || mb.BootID != s.id.BootID || mb.Target != target {
		mb = migrateBlocked{SinceBoot: s.clk.BootMs(), BootID: s.id.BootID, Target: target}
		if nb, merr := json.Marshal(mb); merr != nil || writeAtomic(s.root, name, nb) != nil {
			return false // cannot persist the timer: never force
		}
	}
	return s.clk.BootMs()-mb.SinceBoot >= 600_000
}
