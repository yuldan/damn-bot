//go:build !minimal

package main

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/web"
)

// TestWebListenConflict: the web listener and the Telegram webhook listener
// never share an address; wildcard hosts collide with any host on that port.
func TestWebListenConflict(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		web      string
		opts     serveOptions
		conflict bool
	}{
		{"same loopback", "127.0.0.1:7878", serveOptions{telegramMode: "webhook", telegramWebhookListen: "127.0.0.1:7878"}, true},
		{"wildcard vs loopback", "0.0.0.0:7878", serveOptions{telegramMode: "webhook", telegramWebhookListen: "127.0.0.1:7878"}, true},
		{"loopback vs wildcard", "127.0.0.1:7878", serveOptions{telegramMode: "webhook", telegramWebhookListen: ":7878"}, true},
		{"ipv6 wildcard", "[::]:7878", serveOptions{telegramMode: "webhook", telegramWebhookListen: "127.0.0.1:7878"}, true},
		{"ipv6 loopback", "[::1]:7878", serveOptions{telegramMode: "webhook", telegramWebhookListen: "[::1]:7878"}, true},
		{"different port", "127.0.0.1:7878", serveOptions{telegramMode: "webhook", telegramWebhookListen: "127.0.0.1:7879"}, false},
		{"different host", "127.0.0.1:7878", serveOptions{telegramMode: "webhook", telegramWebhookListen: "127.0.0.2:7878"}, false},
		{"polling mode", "127.0.0.1:7878", serveOptions{telegramMode: "polling", telegramWebhookListen: "127.0.0.1:7878"}, false},
		{"unix web", "unix:<state_dir>/web.sock", serveOptions{telegramMode: "webhook", telegramWebhookListen: "127.0.0.1:7878"}, false},
	}
	for _, tc := range cases {
		err := webListenConflict(tc.web, &tc.opts)
		if tc.conflict {
			if err == nil {
				t.Errorf("%s: no conflict reported", tc.name)
				continue
			}
			if !strings.Contains(err.Error(), tc.web) || !strings.Contains(err.Error(), tc.opts.telegramWebhookListen) {
				t.Errorf("%s: error %q does not name both listeners", tc.name, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected conflict %v", tc.name, err)
		}
	}
}

// TestWebSessionStoreCtxdb proves the web_sessions store: insert, lookup,
// touch, revoke, and the idle/absolute refusals (GAP-SEC-09).
func TestWebSessionStoreCtxdb(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: t.TempDir(), Kind: "serve"})
	if err != nil {
		t.Fatalf("open ctxdb: %v", err)
	}
	t.Cleanup(func() {
		if cerr := db.CloseContext(context.WithoutCancel(ctx)); cerr != nil {
			t.Errorf("close ctxdb: %v", cerr)
		}
	})
	store := &webSessionStore{db: db}
	now := time.Now()
	row := web.Session{
		IDHash: "hash-1", Principal: "owner", Created: now, LastSeen: now,
		AbsoluteExpiry: now.Add(web.AbsoluteTimeout),
	}
	if err := store.Put(ctx, &row); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, ok, gerr := store.Get(ctx, "hash-1")
	if gerr != nil || !ok {
		t.Fatalf("Get = %v, %v", ok, gerr)
	}
	if got.Principal != "owner" || !got.AbsoluteExpiry.Equal(row.AbsoluteExpiry.Truncate(time.Second)) {
		t.Fatalf("row = %+v", got)
	}
	if err := store.Touch(ctx, "hash-1", now.Add(time.Hour)); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	if err := store.Revoke(ctx, "hash-1"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, ok, err := store.Get(ctx, "hash-1"); ok || err != nil {
		t.Fatalf("revoked row = %v, %v", ok, err)
	}
	if _, ok, merr := store.Get(ctx, "missing"); ok || merr != nil {
		t.Fatalf("missing row = %v, %v", ok, merr)
	}
}

// TestWebSessionStoreIdle: an idle-expired row is refused although it is not
// revoked and its absolute cap has not passed.
func TestWebSessionStoreIdle(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: t.TempDir(), Kind: "serve"})
	if err != nil {
		t.Fatalf("open ctxdb: %v", err)
	}
	t.Cleanup(func() {
		if cerr := db.CloseContext(context.WithoutCancel(ctx)); cerr != nil {
			t.Errorf("close ctxdb: %v", cerr)
		}
	})
	store := &webSessionStore{db: db}
	now := time.Now()
	idle := web.Session{
		IDHash: "hash-idle", Principal: "owner", Created: now.Add(-13 * time.Hour),
		LastSeen: now.Add(-13 * time.Hour), AbsoluteExpiry: now.Add(web.AbsoluteTimeout),
	}
	if err := store.Put(ctx, &idle); err != nil {
		t.Fatalf("Put idle: %v", err)
	}
	if _, ok, gerr := store.Get(ctx, "hash-idle"); ok || gerr != nil {
		t.Fatalf("idle row = %v, %v", ok, gerr)
	}
}

// TestKeychainBasicVerify: the basic principal comes from one keychain secret.
func TestKeychainBasicVerify(t *testing.T) {
	ref := "DAMNBOT_TEST_WEB_TOKEN_VALUE"
	t.Setenv(ref, "correct horse")
	basic := keychainBasic{user: "owner", secret: ref}
	if !basic.Verify("owner", "correct horse") {
		t.Fatal("valid credentials refused")
	}
	for _, tc := range [][2]string{{"owner", "wrong"}, {"intruder", "correct horse"}, {"", ""}} {
		if basic.Verify(tc[0], tc[1]) {
			t.Fatalf("credentials %q/%q accepted", tc[0], tc[1])
		}
	}
	unset := "DAMNBOT_TEST_WEB_UNSET_NAME"
	if (keychainBasic{user: "owner", secret: unset}).Verify("owner", "") {
		t.Fatal("empty secret accepted")
	}
}

// TestWebMinimalRefusal also exists in the minimal build, where it asserts the
// opposite answer; here the full build must link the UI.
func TestWebMinimalRefusal(t *testing.T) {
	t.Parallel()
	if !webAvailable() {
		t.Fatal("full build reports the web UI as unavailable")
	}
}

// TestWebSettingsWritesConfig: the writer preserves comments, validates the
// locale and renames atomically.
func TestWebSettingsWritesConfig(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if werr := os.WriteFile(path, []byte("[ui]\nlanguage = \"auto\"\n# keep me\n"), 0o600); werr != nil {
		t.Fatal(werr)
	}
	w, err := newWebSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	diff, err := w.Preview(context.Background(), web.SettingsPatch{Language: "id"})
	if err != nil || !strings.Contains(diff, "language = \"id\"") {
		t.Fatalf("preview diff = %q err %v", diff, err)
	}
	if err := w.Apply(context.Background(), web.SettingsPatch{Language: "id"}); err != nil {
		t.Fatal(err)
	}
	raw, rerr := fs.ReadFile(os.DirFS(filepath.Dir(path)), filepath.Base(path))
	if rerr != nil {
		t.Fatal(rerr)
	}
	if !strings.Contains(string(raw), "language = \"id\"") || !strings.Contains(string(raw), "# keep me") {
		t.Fatalf("config = %s", raw)
	}
	if err := w.Apply(context.Background(), web.SettingsPatch{Language: "xx"}); err == nil {
		t.Fatal("unknown locale accepted")
	}
}
