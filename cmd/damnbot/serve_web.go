//go:build !minimal

package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/web"
)

// webAvailable reports whether this build links the web UI.
func webAvailable() bool { return true }

// registerWebFlag declares `--web`.
func registerWebFlag(fs *flag.FlagSet, web *bool) {
	fs.BoolVar(web, "web", false, "serve the local web UI on [web].listen (SPEC §13.2)")
}

// startServeWeb opens the `[web]` listener and returns its stop function.
func startServeWeb(ctx context.Context, cfg *config.Config, o *serveOptions, dataDir, configFile string, db *ctxdb.Store, stdout, stderr io.Writer) (func(context.Context) error, error) {
	if cerr := webListenConflict(cfg.Web.Listen, o); cerr != nil {
		return nil, cerr
	}
	wc := web.Config{
		Listen:       cfg.Web.Listen,
		AllowRemote:  cfg.Web.AllowRemote,
		AllowedHosts: cfg.Web.AllowedHosts,
		Auth:         cfg.Web.Auth,
		TLSCert:      cfg.Web.TLSCert,
		TLSKey:       cfg.Web.TLSKey,
		DataDir:      dataDir,
		Sessions:     &webSessionStore{db: db},
		Language:     cfg.UI.Language,
	}
	if cfg.Web.Auth == config.WebAuthBasic {
		wc.Basic = keychainBasic{user: cfg.Web.BasicUser, secret: cfg.Web.BasicPasswordSecret}
	}
	settings, serr := newWebSettings(configFile)
	if serr != nil {
		return nil, serr
	}
	wc.Settings = settings
	secret, serr := newControlSecret()
	if serr != nil {
		return nil, serr
	}
	wc.ControlSecret = secret
	srv, err := web.New(&wc)
	if err != nil {
		return nil, fmt.Errorf("web ui: %w", err)
	}
	ln, err := srv.Listen(ctx)
	if err != nil {
		return nil, fmt.Errorf("web ui: %w", err)
	}
	go func() {
		if serr := srv.Serve(ln); serr != nil {
			sessionsPrintf(stderr, "web ui: %s\n", cleanLine(serr.Error()))
		}
	}()
	if werr := writeWebState(dataDir, srv.Addr(), secret); werr != nil {
		return nil, fmt.Errorf("web state: %w", werr)
	}
	sessionsPrintf(stdout, "web: listening on %s\n", srv.Addr())
	sessionsPrintf(stdout, "web: open %s\n", srv.OpenURL(cfg.Web.AllowRemote))
	return func(ctx context.Context) error {
		if rerr := removeWebState(dataDir); rerr != nil {
			sessionsPrintf(stderr, "web ui: %s\n", cleanLine(rerr.Error()))
		}
		return srv.Shutdown(ctx)
	}, nil
}

// keychainBasic verifies the single basic-auth principal against a keychain
// secret name; the value never lives in config.
type keychainBasic struct {
	user, secret string
}

// Verify is constant-time over the secret and the user name.
func (k keychainBasic) Verify(user, password string) bool {
	if k.secret == "" {
		return false
	}
	want := config.LookupSecret(k.secret)
	if want == "" {
		return false
	}
	userOK := subtle.ConstantTimeCompare([]byte(user), []byte(k.user))
	passOK := subtle.ConstantTimeCompare([]byte(password), []byte(want))
	return userOK&passOK == 1
}

// webSessionStore is the ctxdb implementation of web.SessionStore over the
// mutable web_sessions table (migration 0007, GAP-SEC-09).
type webSessionStore struct{ db *ctxdb.Store }

// Put inserts or refreshes one session row.
func (w *webSessionStore) Put(ctx context.Context, s *web.Session) error {
	err := w.db.Write(ctx, func(tx *ctxdb.Tx) error {
		if _, err := tx.Exec(`INSERT INTO web_sessions
			(id_hash, principal, created_at, last_seen_at, absolute_expiry, revoked_at)
			VALUES (?1, ?2, ?3, ?4, ?5, NULL)
			ON CONFLICT(id_hash) DO UPDATE SET principal = excluded.principal,
				created_at = excluded.created_at, last_seen_at = excluded.last_seen_at,
				absolute_expiry = excluded.absolute_expiry, revoked_at = NULL`,
			s.IDHash, s.Principal, s.Created.Unix(), s.LastSeen.Unix(), s.AbsoluteExpiry.Unix()); err != nil {
			return fmt.Errorf("insert session: %w", err)
		}
		return nil
	})
	return wrapWebErr("put", err)
}

// Get returns a row only while it is neither revoked nor expired.
func (w *webSessionStore) Get(ctx context.Context, idHash string) (web.Session, bool, error) {
	var (
		s              web.Session
		created, last  int64
		expiry, revoke int64
	)
	err := w.db.Write(ctx, func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT principal, created_at, last_seen_at, absolute_expiry,
			coalesce(revoked_at, 0) FROM web_sessions WHERE id_hash = ?1`, []any{idHash},
			&s.Principal, &created, &last, &expiry, &revoke)
	})
	if errors.Is(err, ctxdb.ErrNotFound) {
		return web.Session{}, false, nil
	}
	if err != nil {
		return web.Session{}, false, wrapWebErr("get", err)
	}
	s.IDHash = idHash
	s.Created, s.LastSeen = unixTime(created), unixTime(last)
	s.AbsoluteExpiry = unixTime(expiry)
	now := time.Now()
	if revoke != 0 || !now.Before(s.AbsoluteExpiry) || now.Sub(s.LastSeen) > web.IdleTimeout {
		return web.Session{}, false, nil
	}
	return s, true, nil
}

// Touch records activity; the absolute expiry is never extended.
func (w *webSessionStore) Touch(ctx context.Context, idHash string, at time.Time) error {
	err := w.db.Write(ctx, func(tx *ctxdb.Tx) error {
		if _, err := tx.Exec(`UPDATE web_sessions SET last_seen_at = ?2 WHERE id_hash = ?1`, idHash, at.Unix()); err != nil {
			return fmt.Errorf("touch session: %w", err)
		}
		return nil
	})
	return wrapWebErr("touch", err)
}

// Revoke stamps revoked_at; the next lookup fails.
func (w *webSessionStore) Revoke(ctx context.Context, idHash string) error {
	err := w.db.Write(ctx, func(tx *ctxdb.Tx) error {
		if _, err := tx.Exec(`UPDATE web_sessions SET revoked_at = ?2 WHERE id_hash = ?1 AND revoked_at IS NULL`,
			idHash, time.Now().Unix()); err != nil {
			return fmt.Errorf("revoke session: %w", err)
		}
		return nil
	})
	return wrapWebErr("revoke", err)
}

// unixTime converts a web_sessions epoch second to UTC.
func unixTime(sec int64) time.Time { return time.Unix(sec, 0).UTC() }

func wrapWebErr(op string, err error) error {
	if err != nil {
		return fmt.Errorf("web sessions %s: %w", op, err)
	}
	return nil
}

// webListenConflict refuses to start two listeners of this process on one
// address (SPEC §13.2 pins the web UI at 127.0.0.1:7878 and the Telegram
// webhook flag defaults to the same address).
func webListenConflict(webAddr string, o *serveOptions) error {
	if o.telegramMode != "webhook" || o.telegramWebhookListen == "" {
		return nil
	}
	if sameListenAddr(webAddr, o.telegramWebhookListen) {
		return fmt.Errorf("web ui: listen %q collides with the Telegram webhook listener %q; set [web] listen or --telegram-webhook-listen to different addresses", webAddr, o.telegramWebhookListen)
	}
	return nil
}

// sameListenAddr reports a collision when the ports match and either host is a
// wildcard (empty, 0.0.0.0, ::) or both hosts are identical.
func sameListenAddr(a, b string) bool {
	ha, pa, ea := net.SplitHostPort(a)
	hb, pb, eb := net.SplitHostPort(b)
	if ea != nil || eb != nil || pa != pb {
		return false
	}
	return wildcardHost(ha) || wildcardHost(hb) || ha == hb
}

// wildcardHost is an all-interfaces bind.
func wildcardHost(host string) bool {
	return host == "" || host == "0.0.0.0" || host == "::"
}

// webSettings previews and applies `[ui]` changes through the same atomic
// config writer as the CLI (comments preserved, validation before rename).
type webSettings struct {
	loader *config.Loader
	path   string
}

// newWebSettings builds the writer for the running profile's config file.
func newWebSettings(path string) (*webSettings, error) {
	loader, err := config.New(config.OS())
	if err != nil {
		return nil, fmt.Errorf("config deps: %w", err)
	}
	return &webSettings{loader: loader, path: path}, nil
}

// Preview validates the patch and returns a two-line diff.
func (w *webSettings) Preview(_ context.Context, patch web.SettingsPatch) (string, error) {
	_, diff, err := w.render(patch)
	return diff, err
}

// Apply writes the validated patch atomically.
func (w *webSettings) Apply(_ context.Context, patch web.SettingsPatch) error {
	updated, _, err := w.render(patch)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(w.path), ".config.toml.web-*")
	if err != nil {
		return fmt.Errorf("temp config: %w", err)
	}
	name := tmp.Name()
	if _, werr := tmp.Write(updated); werr != nil {
		if cerr := tmp.Close(); cerr != nil {
			return fmt.Errorf("write temp config: %w (close: %w)", werr, cerr)
		}
		return fmt.Errorf("write temp config: %w", werr)
	}
	if cerr := tmp.Close(); cerr != nil {
		return fmt.Errorf("close temp config: %w", cerr)
	}
	return commitConfig(name, w.path)
}

// render applies the patch in memory and validates the result.
func (w *webSettings) render(patch web.SettingsPatch) ([]byte, string, error) {
	raw, err := os.ReadFile(w.path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, "", fmt.Errorf("read config: %w", err)
	}
	updated := upsertSection(raw, "[ui]", [][2]string{{"language", strconv.Quote(patch.Language)}})
	if _, perr := w.loader.Parse(updated, w.path); perr != nil {
		return nil, "", fmt.Errorf("validate config: %w", perr)
	}
	return updated, settingsDiff(raw, updated), nil
}

// settingsDiff renders the changed `language` lines as a minimal diff.
func settingsDiff(oldData, newData []byte) string {
	oldLine, newLine := `language = ""`, `language = ""`
	for line := range strings.SplitSeq(string(oldData), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "language") {
			oldLine = strings.TrimSpace(line)
		}
	}
	for line := range strings.SplitSeq(string(newData), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "language") {
			newLine = strings.TrimSpace(line)
		}
	}
	return "- " + oldLine + "\n+ " + newLine + "\n"
}
