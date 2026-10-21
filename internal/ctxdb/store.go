// Package ctxdb is the SQLite store of Damn-Bot: DDL SPEC §11 (0001–0008),
// session lease + fencing, purge, blob gc, journal recovery and the
// maintenance barrier, following the single normative model SPEC §11.11.
// It is the only package importing database/sql and modernc.org/sqlite (E-04).
package ctxdb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"modernc.org/sqlite"
)

// SystemSession owns audit/system rows (SPEC §11.11.10); never purged.
const SystemSession = "__system__"

const dbFile = "damnbot.db"

// Options configures Open. Zero values select production defaults.
type Options struct {
	Dir          string // data dir: damnbot.db, blobs/, run/, purge.log
	Kind         string // tui|acp|serve|run|cli|mcp_serve|doctor (holder kind)
	Clock        Clock
	Procs        Procs
	Ident        *Ident        // override (tests simulating processes)
	SchemaMax    int           // newest migration this binary knows; 0 = all embedded
	ForceMigrate bool          // --force-migrate
	BusyTimeout  time.Duration // default 5 s (SPEC §11 PRAGMA)
	BarrierWait  time.Duration // drain timeout of the maintenance barrier; default 30 s
	// OnTx observes every write transaction: mode and lock hold time
	// (BEGIN IMMEDIATE → COMMIT/ROLLBACK), for I11 budgets and metrics.
	OnTx func(mode string, held time.Duration)
}

// Store is one process's handle on the data dir: one writer connection
// (single writer per process, SPEC §3.3) plus a read-only pool.
type Store struct {
	opt  Options
	id   Ident
	dir  string
	root *os.Root // every file operation stays inside the data dir
	clk  Clock
	q    map[string]string // named statements of queries.sql
	migs []migration       // embedded migration ladder
	mu   sync.Mutex        // serialises write transactions (the process writer)
	w, r *sql.DB
	fi   os.FileInfo // identity of the opened DB file (os.SameFile, I8)
	lock *os.File    // run/maint.lock held shared while open (§11.11.7)

	mode   atomic.Value // dmb_maint() of the writer connection: "", "purge", "restore"
	tokMu  sync.Mutex
	tokens map[string]int64 // dmb_lease_token(): session → fence token held
	obsMu  sync.Mutex
	obs    map[string]observation // takeover_ok() case (b) observations
	txSeq  atomic.Int64
	// barrierOwner: this Store owns the maintenance barrier (restore), so the
	// marker must not drain it; also set while a purge releases its lease.
	barrierOwner atomic.Bool
	purgeOrder   map[string][]string // txid → labelled sessions, descendants first (mu)
	chunkCap     int                 // upper bound of chunk (eventBatch; tests lower it)
	budget       time.Duration       // per-transaction work budget (unitBudget; tests lower it)
	chunk        int                 // purge phase-2 unit size, adapted to measured cost (mu)
	closed       bool                // Close ran (mu)
	bgErr        error               // error of a background drain, returned by Close (mu)
	yieldUntil   time.Time           // writer fairness: no BEGIN before this (mu, ADR-087)
	stop         context.CancelFunc  // barrier watcher
	done         chan struct{}
}

// DBPath returns the database file of a data dir.
func DBPath(dir string) string { return filepath.Join(dir, dbFile) }

// Open runs the open sequence: barrier check, migration (under barrier
// reason=migrate), shared maint.lock, then startup recovery (SELF-HEALING §3.4).
func Open(ctx context.Context, o *Options) (*Store, error) {
	s, err := newStore(o)
	if err == nil {
		err = s.migrateIfNeeded(ctx)
	}
	if err == nil {
		err = s.attach(ctx)
	}
	if err == nil {
		err = s.startupRecovery(ctx)
	}
	if err != nil {
		if s != nil {
			err = errors.Join(err, s.close(ctx))
		}
		return nil, err
	}
	wctx, stop := context.WithCancel(context.WithoutCancel(ctx))
	s.stop, s.done = stop, make(chan struct{})
	go s.watch(wctx)
	return s, nil
}

func newStore(o *Options) (*Store, error) {
	opt := *o
	if opt.Clock == nil {
		opt.Clock = SystemClock{}
	}
	if opt.Procs == nil {
		opt.Procs = SystemProcs{}
	}
	opt.BusyTimeout = cmpOr(opt.BusyTimeout, 5*time.Second)
	opt.BarrierWait = cmpOr(opt.BarrierWait, BarrierWait)
	q, err := loadQueries()
	if err != nil {
		return nil, err
	}
	migs, err := migrations()
	if err != nil {
		return nil, err
	}
	root, err := openRoot(opt.Dir)
	if err != nil {
		return nil, err
	}
	s := &Store{
		opt: opt, dir: opt.Dir, root: root, clk: opt.Clock, q: q, migs: migs, chunk: firstChunk, chunkCap: eventBatch, budget: unitBudget,
		tokens: map[string]int64{}, obs: map[string]observation{},
	}
	s.mode.Store(modeNormal)
	s.id = SelfIdent()
	if opt.Ident != nil {
		s.id = *opt.Ident
	}
	return s, nil
}

func cmpOr(v, def time.Duration) time.Duration {
	if v == 0 {
		return def
	}
	return v
}

// watch drains an idle Store within one poll when a maintenance marker
// appears (§11.11.7 "dicek … tiap 1 s"); busy Stores drain in preWrite.
func (s *Store) watch(ctx context.Context) {
	defer close(s.done)
	tk := time.NewTicker(barrierPoll)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
		}
		if markerPresent(s.root) && !s.barrierOwner.Load() {
			s.mu.Lock()
			s.bgErr = errors.Join(s.bgErr, s.detach())
			s.mu.Unlock()
		}
	}
}

// attach = barrier-respecting open of the SQL handles (§11.11.7 order:
// marker → shared maint.lock → marker → open), then holder registration.
func (s *Store) attach(ctx context.Context) error {
	lk, err := s.waitSharedLock(ctx)
	if err != nil {
		return err
	}
	s.lock = lk
	if s.w, s.r, err = s.openHandles(ctx); err == nil {
		s.fi, err = s.root.Stat(dbFile)
	}
	if err == nil {
		err = s.registerHolder()
	}
	if err != nil {
		return errors.Join(wrap(err), s.detach())
	}
	return nil
}

func (s *Store) dsn(readOnly bool) string {
	q := []string{
		fmt.Sprintf("_pragma=busy_timeout(%d)", s.opt.BusyTimeout.Milliseconds()),
		"_pragma=journal_mode(WAL)", "_pragma=synchronous(FULL)", "_pragma=foreign_keys(1)",
		"_pragma=temp_store(MEMORY)", "_pragma=journal_size_limit(67108864)", "_pragma=wal_autocheckpoint(1000)",
		"_pragma=trusted_schema(1)", // ADR-093 (TS-P03): trigger functions cannot be SQLITE_INNOCUOUS
	}
	if readOnly {
		q = append(q, "_pragma=query_only(1)", "_pragma=cache_size(-12288)")
	}
	return "file:" + DBPath(s.dir) + "?" + strings.Join(q, "&")
}

func (s *Store) openHandles(ctx context.Context) (w, r *sql.DB, err error) {
	dw, errW := s.driver(true)
	dr, errR := s.driver(false)
	if err = errors.Join(errW, errR); err != nil {
		return nil, nil, wrap(err)
	}
	w = sql.OpenDB(connector{d: dw, dsn: s.dsn(false), s: s})
	w.SetMaxOpenConns(1)
	w.SetConnMaxLifetime(0)
	r = sql.OpenDB(connector{d: dr, dsn: s.dsn(true)})
	r.SetMaxOpenConns(4)
	if err = w.PingContext(ctx); err == nil {
		err = r.PingContext(ctx)
	}
	if err != nil {
		return nil, nil, wrap(errors.Join(err, w.Close(), r.Close()))
	}
	return w, r, nil
}

// driver registers the per-connection functions of SPEC §11 on a private
// Driver so dmb_maint()/dmb_lease_token() see this Store's state only.
func (s *Store) driver(writer bool) (*sqlite.Driver, error) {
	d := &sqlite.Driver{}
	return d, errors.Join(
		d.RegisterScalarFunction("dmb_maint", 0, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
			if !writer {
				return modeNormal, nil
			}
			return s.mode.Load(), nil
		}),
		d.RegisterScalarFunction("dmb_lease_token", 1, func(_ *sqlite.FunctionContext, a []driver.Value) (driver.Value, error) {
			sid, ok := a[0].(string)
			if t, held := s.token(sid); ok && held && writer {
				return t, nil
			}
			return nil, nil
		}),
		d.RegisterDeterministicScalarFunction("dmb_fts_norm", 1, func(_ *sqlite.FunctionContext, a []driver.Value) (driver.Value, error) {
			t, ok := a[0].(string)
			if !ok {
				return "", nil // non-text input normalises to ""
			}
			return ftsNorm(t), nil
		}))
}

// connector opens driver connections; for the writer (s != nil) it installs
// commit/rollback hooks that force the mode back to ” (§11.11.3, tx()
// also resets it in a defer) and creates the purge scratch tables.
type connector struct {
	d   *sqlite.Driver
	dsn string
	s   *Store
}

func (c connector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.d.Open(c.dsn)
	if err != nil || c.s == nil {
		return conn, wrap(err)
	}
	if h, ok := conn.(sqlite.HookRegisterer); ok {
		h.RegisterCommitHook(func() int32 { c.s.mode.Store(modeNormal); return 0 })
		h.RegisterRollbackHook(func() { c.s.mode.Store(modeNormal) })
	}
	if ex, ok := conn.(driver.ExecerContext); ok {
		if _, err := ex.ExecContext(ctx, c.s.q["temp.tables"], nil); err != nil {
			err = wrap(err)
			err = errors.Join(err, wrap(conn.Close()))
			return nil, err
		}
	}
	return conn, nil
}

func (c connector) Driver() driver.Driver { return c.d }

// ftsNorm lowercases text and splits CJK runs into overlapping bigrams
// (INF-01) so unicode61 tokenises them.
func ftsNorm(t string) string {
	var b strings.Builder
	var prev rune = -1
	for _, r := range strings.ToLower(t) {
		cjk := unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
		if prev >= 0 {
			b.WriteRune(prev)
			if cjk {
				b.WriteRune(r)
			}
			b.WriteByte(' ')
		}
		prev = -1
		if cjk {
			prev = r
		} else {
			b.WriteRune(r)
		}
	}
	if prev >= 0 {
		b.WriteRune(prev)
	}
	return b.String()
}

// detach closes handles and releases the shared barrier lock (drain).
func (s *Store) detach() error {
	var err error
	if s.w != nil {
		err = wrap(errors.Join(s.w.Close(), s.r.Close()))
		s.w, s.r = nil, nil
	}
	if s.lock != nil {
		err = errors.Join(err, unlockFile(s.lock))
		s.lock = nil
	}
	s.tokMu.Lock()
	clear(s.tokens) // drained processes discard lease tokens (§11.11.7)
	s.tokMu.Unlock()
	return err
}

// Close releases the holder entry, handles and barrier lock. The last
// process out truncates the WAL (SPEC §11). "Last" is proven by taking
// run/maint.lock exclusively (every open process holds it shared): a
// TRUNCATE checkpoint holds the writer lock while it waits for readers, so
// running it beside live processes stalls their BEGIN IMMEDIATE past
// busy_timeout (root cause of SQLITE_BUSY in TestMultiProcess under -race).
// Close is close without a caller context (the process is shutting down).
func (s *Store) Close() error { return s.close(context.Background()) }

// CloseContext closes the store using the caller's context for the shutdown
// barrier. Prefer it where a context is already in scope.
func (s *Store) CloseContext(ctx context.Context) error { return s.close(ctx) }

func (s *Store) close(ctx context.Context) error {
	if s.stop != nil {
		s.stop()
		<-s.done
		s.stop = nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed { // idempotent: a second Close is a no-op
		return nil
	}
	s.closed = true
	err := s.bgErr
	if s.w != nil && s.lock != nil {
		err = errors.Join(err, unlockFile(s.lock))
		s.lock = nil
		if last, lerr := tryLock(s.root, lockFile, true); lerr == nil {
			_, cerr := s.w.ExecContext(context.WithoutCancel(ctx), "PRAGMA wal_checkpoint(TRUNCATE)")
			err = errors.Join(err, wrap(cerr), unlockFile(last))
		}
	}
	err = errors.Join(err, removeIfExists(s.root, s.holderName()), s.detach(), wrap(s.root.Close()))
	return err
}

// Ident returns the identity this Store writes into leases.
func (s *Store) Ident() Ident { return s.id }

// Dir returns the data dir.
func (s *Store) Dir() string { return s.dir }

// IntegrityCheck runs PRAGMA integrity_check on a fresh read-only
// connection. A pooled reader that already ran an FTS5 integrity check can
// keep a stale FTS5 structure cache after another connection rewrites the
// index (contentless_delete purge, SQLite 3.53.4): a later check on that
// connection reports "fts5: corruption found" although the file is intact
// (TS-P11). Reusing it would make SELF-HEALING auto-restore a healthy DB.
func (s *Store) IntegrityCheck(ctx context.Context) (string, error) {
	d, err := s.driver(false)
	if err != nil {
		return "", wrap(err)
	}
	db := sql.OpenDB(connector{d: d, dsn: s.dsn(true)})
	var res string
	err = getOne(ctx, db, "PRAGMA integrity_check", nil, &res)
	return res, errors.Join(err, wrap(db.Close()))
}
