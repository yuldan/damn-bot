//go:build !minimal

package main

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// TestWebStateRoundTrip: the control record is 0600, round-trips, and is
// removed on shutdown.
func TestWebStateRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := writeWebState(dir, "127.0.0.1:7878", "s3cret"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(webStatePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	// Windows has no POSIX permission bits (os.Stat reports 0666), so the
	// 0600 contract is asserted on the platforms that enforce it; the
	// round-trip and removal checks below still run everywhere.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	st, err := readWebState(dir)
	if err != nil || st.Addr != "127.0.0.1:7878" || st.Control != "s3cret" {
		t.Fatalf("state = %+v err %v", st, err)
	}
	if rerr := removeWebState(dir); rerr != nil {
		t.Fatal(rerr)
	}
	if _, err := readWebState(dir); err == nil {
		t.Fatal("state survived removal")
	}
}

// TestServeSessionsRevoke: --all and a hex prefix both revoke in the DB.
func TestServeSessionsRevoke(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ctx := context.Background()
	db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: dir, Kind: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	err = db.Write(ctx, func(tx *ctxdb.Tx) error {
		_, xerr := tx.Exec(`INSERT INTO web_sessions (id_hash, principal, created_at, last_seen_at, absolute_expiry)
			VALUES (?1, 'owner', ?2, ?2, ?3)`, "abcdef0123456789", now, now+3600)
		if xerr != nil {
			return fmt.Errorf("insert session: %w", xerr)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if cerr := db.CloseContext(ctx); cerr != nil {
		t.Fatal(cerr)
	}
	if code := serveSessionsRevoke([]string{"--data-dir", dir, "--all"}, os.Stdout, os.Stderr); code != 0 {
		t.Fatalf("revoke --all code = %d", code)
	}
	db, err = ctxdb.Open(ctx, &ctxdb.Options{Dir: dir, Kind: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	var revoked int64
	err = db.Write(ctx, func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT coalesce(revoked_at,0) FROM web_sessions WHERE id_hash = ?1`, []any{"abcdef0123456789"}, &revoked)
	})
	if cerr := db.CloseContext(ctx); cerr != nil {
		t.Fatal(cerr)
	}
	if err != nil || revoked == 0 {
		t.Fatalf("revoke --all: revoked=%d err=%v", revoked, err)
	}
}

// TestServeOpenRefusesWithoutState: no running daemon is a typed error.
func TestServeOpenRefusesWithoutState(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if code := runServeOpen([]string{"--data-dir", dir}, os.Stdout, os.Stderr); code == 0 {
		t.Fatal("open succeeded without a daemon")
	}
	if _, err := readWebState(dir); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("err = %v", err)
	}
}
