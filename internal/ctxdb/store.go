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

// Options configures Open. Zero values select production defaults.
type Options struct {
	Dir          string // data dir: damnbot.db, blobs/, run/, purge.log
	Kind         string // tui|acp|serve|run|cli|mcp_serve (holder kind)
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
	clk  Clock
	mu   sync.Mutex // serialises write transactions (the process writer)
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
	purgeOrder   map[string][]string // txid → labelled sessions, descendants first (guarded by mu)
	q            map[string]string   // named statements of queries.sql
	stop         context.CancelFunc // barrier watcher
	done         chan struct{}
}

// DBPath returns the database file of a data dir.
func DBPath(dir string) string { return filepath.Join(dir, "damnbot.db") }

// Open runs the open sequence: barrier check, migration (under barrier
// reason=migrate), shared maint.lock, then startup recovery (SELF-HEALING §3.4).
func Open(ctx context.Context, o Options) (*Store, error) {
	s, err := newStore(o)
	if err != nil {
		return nil, err
	}
	if err := s.migrateIfNeeded(ctx); err != nil {
		return nil, err
	}
	if err := s.attach(ctx); err != nil {
		return nil, err
	}
	if err := s.startupRecovery(ctx); err != nil {
		return nil, errors.Join(err, s.Close())
	}
	wctx, stop := context.WithCancel(context.WithoutCancel(ctx))
	s.stop, s.done = stop, make(chan struct{})
	go s.watch(wctx)
	return s, nil
}

func newStore(o Options) (*Store, error) {
	if o.Clock == nil {
		o.Clock = SystemClock{}
	}
	if o.Procs == nil {
		o.Procs = SystemProcs{}
	}
	if o.BusyTimeout == 0 {
		o.BusyTimeout = 5 * time.Second
	}
	if o.BarrierWait == 0 {
		o.BarrierWait = BarrierWait
	}
	q, err := loadQueries()
	if err != nil {
		return nil, fmt.Errorf("ctxdb: queries: %w", err)
	}
	s := &Store{opt: o, dir: o.Dir, clk: o.Clock, q: q, tokens: map[string]int64{}, obs: map[string]observation{}}
	s.mode.Store("")
	if o.Ident != nil {
		s.id = *o.Ident
	} else {
		s.id = SelfIdent()
	}
	for _, d := range []string{"blobs/tmp", "blobs/.trash", "run/maint.holders", "backups"} {
		if err := os.MkdirAll(filepath.Join(o.Dir, d), 0o700); err != nil {
			return nil, fmt.Errorf("ctxdb: mkdir: %w", err)
		}
	}
	return s, nil
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
			if markerPresent(s.dir) && !s.barrierOwner.Load() {
				s.mu.Lock()
				if s.w != nil {
					s.detach()
				}
				s.mu.Unlock()
			}
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
	if s.w, s.r, err = s.openHandles(); err != nil {
		s.detach()
		return err
	}
	if s.fi, err = os.Stat(DBPath(s.dir)); err != nil {
		s.detach()
		return fmt.Errorf("ctxdb: stat db: %w", err)
	}
	return s.registerHolder()
}

func (s *Store) dsn(readOnly bool) string {
	q := []string{
		fmt.Sprintf("_pragma=busy_timeout(%d)", s.opt.BusyTimeout.Milliseconds()),
		"_pragma=journal_mode(WAL)", "_pragma=synchronous(FULL)", "_pragma=foreign_keys(1)",
		"_pragma=temp_store(MEMORY)", "_pragma=journal_size_limit(67108864)",
		"_pragma=wal_autocheckpoint(1000)",
	}
	if readOnly {
		q = append(q, "_pragma=query_only(1)")
	}
	return "file:" + DBPath(s.dir) + "?" + strings.Join(q, "&")
}

func (s *Store) openHandles() (w, r *sql.DB, err error) {
	dw, errW := s.driver(true)
	dr, errR := s.driver(false)
	if err = errors.Join(errW, errR); err != nil {
		return nil, nil, fmt.Errorf("ctxdb: register functions: %w", err)
	}
	w = sql.OpenDB(connector{dw, s.dsn(false)})
	w.SetMaxOpenConns(1)
	w.SetConnMaxLifetime(0)
	r = sql.OpenDB(connector{dr, s.dsn(true)})
	r.SetMaxOpenConns(4)
	if err = w.Ping(); err == nil {
		err = r.Ping()
	}
	if err != nil {
		err = errors.Join(err, w.Close(), r.Close())
		return nil, nil, fmt.Errorf("ctxdb: open: %w", classify(err))
	}
	return w, r, nil
}

// driver registers the per-connection functions of SPEC §11 on a private
// Driver so dmb_maint()/dmb_lease_token() see this Store's state only.
// Writer connections also get commit/rollback hooks that force the mode
// back to '' (§11.11.3); tx() additionally resets it in a defer.
func (s *Store) driver(writer bool) (*sqlite.Driver, error) {
	d := &sqlite.Driver{}
	err := errors.Join(
		d.RegisterScalarFunction("dmb_maint", 0, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
			if !writer {
				return "", nil
			}
			return s.mode.Load(), nil
		}),
		d.RegisterScalarFunction("dmb_lease_token", 1, func(_ *sqlite.FunctionContext, a []driver.Value) (driver.Value, error) {
			sid, _ := a[0].(string)
			s.tokMu.Lock()
			defer s.tokMu.Unlock()
			if t, ok := s.tokens[sid]; ok && writer {
				return t, nil
			}
			return nil, nil
		}),
		d.RegisterDeterministicScalarFunction("dmb_fts_norm", 1, func(_ *sqlite.FunctionContext, a []driver.Value) (driver.Value, error) {
			t, _ := a[0].(string)
			return ftsNorm(t), nil
		}))
	if writer {
		d.RegisterConnectionHook(func(c sqlite.ExecQuerierContext, _ string) error {
			if h, ok := c.(sqlite.HookRegisterer); ok {
				h.RegisterCommitHook(func() int32 { s.mode.Store(""); return 0 })
				h.RegisterRollbackHook(func() { s.mode.Store("") })
			}
			_, err := c.ExecContext(context.Background(), s.q["temp.tables"], nil) // purge scratch tables
			return err
		})
	}
	return d, err
}

type connector struct {
	d   *sqlite.Driver
	dsn string
}

func (c connector) Connect(context.Context) (driver.Conn, error) { return c.d.Open(c.dsn) }
func (c connector) Driver() driver.Driver                        { return c.d }

// ftsNorm lowercases text and splits CJK runs into overlapping bigrams
// (INF-01) so unicode61 tokenises them.
func ftsNorm(t string) string {
	var b strings.Builder
	var prev rune = -1
	for _, r := range strings.ToLower(t) {
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
			if prev >= 0 {
				b.WriteRune(prev)
				b.WriteRune(r)
				b.WriteByte(' ')
			}
			prev = r
			continue
		}
		if prev >= 0 {
			b.WriteRune(prev)
			b.WriteByte(' ')
			prev = -1
		}
		b.WriteRune(r)
	}
	if prev >= 0 {
		b.WriteRune(prev)
	}
	return b.String()
}

// detach closes handles and releases the shared barrier lock (drain).
func (s *Store) detach() {
	if s.w != nil {
		_ = s.w.Close()
		_ = s.r.Close()
		s.w, s.r = nil, nil
	}
	if s.lock != nil {
		unlockFile(s.lock)
		s.lock = nil
	}
	s.tokMu.Lock()
	clear(s.tokens) // drained processes discard lease tokens (§11.11.7)
	s.tokMu.Unlock()
}

// Close releases leases' handles, the holder entry and the barrier lock.
// The last process out truncates the WAL (SPEC §11).
func (s *Store) Close() error {
	if s.stop != nil {
		s.stop()
		<-s.done
		s.stop = nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.w != nil {
		_, _ = s.w.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	}
	_ = os.Remove(s.holderPath())
	s.detach()
	return nil
}

// Ident returns the identity this Store writes into leases.
func (s *Store) Ident() Ident { return s.id }

// Dir returns the data dir.
func (s *Store) Dir() string { return s.dir }

var errNoRows = sql.ErrNoRows

// isNoRows reports a missing row from QueryRow.Scan.
func isNoRows(err error) bool { return errors.Is(err, errNoRows) }
