//go:build !minimal

package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/netx"
)

// webState is the loopback control record `serve --web` writes 0600 so
// `serve open` can mint a one-use URL from the running process (GAP-SEC-07).
type webState struct {
	Addr    string `json:"addr"`
	Control string `json:"control"`
}

// webStatePath is the control file inside the data dir.
func webStatePath(dataDir string) string { return filepath.Join(dataDir, "web.open") }

// writeWebState stores the control record 0600 (secret never logged).
func writeWebState(dataDir, addr, secret string) error {
	raw, err := json.Marshal(webState{Addr: addr, Control: secret})
	if err != nil {
		return fmt.Errorf("marshal web state: %w", err)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("web state dir: %w", err)
	}
	if err := os.WriteFile(webStatePath(dataDir), raw, 0o600); err != nil {
		return fmt.Errorf("write web state: %w", err)
	}
	return nil
}

// removeWebState deletes the control record on shutdown.
func removeWebState(dataDir string) error {
	if err := os.Remove(webStatePath(dataDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove web state: %w", err)
	}
	return nil
}

// readWebState loads the control record.
func readWebState(dataDir string) (webState, error) {
	raw, err := os.ReadFile(webStatePath(dataDir))
	if err != nil {
		return webState{}, fmt.Errorf("web UI is not running (no %s): %w", webStatePath(dataDir), err)
	}
	var st webState
	if err := json.Unmarshal(raw, &st); err != nil {
		return webState{}, fmt.Errorf("web state: %w", err)
	}
	if st.Addr == "" || st.Control == "" {
		return webState{}, errors.New("web state is incomplete; restart serve")
	}
	return st, nil
}

// newControlSecret returns a 256-bit control secret.
func newControlSecret() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("control secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// runServeSub dispatches `serve open` and `serve sessions` before the daemon
// flags are parsed. handled=false means the daemon owns the arguments.
func runServeSub(args []string, stdout, stderr io.Writer) (int, bool) {
	if len(args) == 0 {
		return 0, false
	}
	switch args[0] {
	case "open":
		return runServeOpen(args[1:], stdout, stderr), true
	case "sessions":
		return runServeSessions(args[1:], stdout, stderr), true
	}
	return 0, false
}

// runServeOpen asks the running daemon for a one-use URL (local TTL 60 s,
// --remote 10 min) and prints it once.
func runServeOpen(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve open", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var dataDir, configPath string
	var remote bool
	fs.StringVar(&dataDir, "data-dir", "", "data directory (default: SPEC §11)")
	fs.StringVar(&configPath, "config", "", "config.toml path")
	fs.BoolVar(&remote, "remote", false, "mint a remote link (10 min TTL)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dir, err := resolveWebDataDir(dataDir)
	if err != nil {
		return serveSubFail(stderr, "serve open", err)
	}
	st, err := readWebState(dir)
	if err != nil {
		return serveSubFail(stderr, "serve open", err)
	}
	target := "http://" + st.Addr + "/auth/link"
	if remote {
		target += "?remote=1"
	}
	// The control endpoint is the daemon's loopback listener, not outbound
	// egress: use the proxy-free loopback client instead of a registry entry.
	client := netx.LoopbackClient()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, target, nil)
	if err != nil {
		return serveSubFail(stderr, "serve open", err)
	}
	req.Header.Set("X-Damnbot-Control", st.Control)
	resp, err := client.Do(req)
	if err != nil {
		return serveSubFail(stderr, "serve open", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			sessionsPrintf(stderr, "serve open: close: %s\n", cleanLine(cerr.Error()))
		}
	}()
	if resp.StatusCode != http.StatusOK {
		return serveSubFail(stderr, "serve open", fmt.Errorf("daemon answered %s", resp.Status))
	}
	var body struct {
		URL string `json:"url"`
	}
	if derr := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&body); derr != nil {
		return serveSubFail(stderr, "serve open", derr)
	}
	u, err := url.Parse(body.URL)
	if err != nil || u.Fragment == "" {
		return serveSubFail(stderr, "serve open", errors.New("daemon returned no one-use URL"))
	}
	sessionsPrintf(stdout, "%s\n", body.URL)
	return 0
}

// runServeSessions lists or revokes web UI sessions (CLI-069, GAP-SEC-09).
func runServeSessions(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		sessionsPrintf(stderr, "usage: damnbot serve sessions ls|revoke [--all|<id>]\n")
		return 2
	}
	switch args[0] {
	case "ls":
		return serveSessionsList(args[1:], stdout, stderr)
	case "revoke":
		return serveSessionsRevoke(args[1:], stdout, stderr)
	default:
		sessionsPrintf(stderr, "serve sessions: unknown action %q\n", cleanArg(args[0]))
		return 2
	}
}

// serveSessionsList prints one line per web session row.
func serveSessionsList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve sessions ls", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var dataDir, configPath string
	fs.StringVar(&dataDir, "data-dir", "", "data directory")
	fs.StringVar(&configPath, "config", "", "config.toml path")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dir, err := resolveWebDataDir(dataDir)
	if err != nil {
		return serveSubFail(stderr, "serve sessions", err)
	}
	db, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: dir, Kind: "cli"})
	if err != nil {
		return serveSubFail(stderr, "serve sessions", err)
	}
	defer func() {
		if cerr := db.CloseContext(context.Background()); cerr != nil {
			sessionsPrintf(stderr, "serve sessions: close: %s\n", cleanLine(cerr.Error()))
		}
	}()
	var rows int
	err = db.Write(context.Background(), func(tx *ctxdb.Tx) error {
		ids, lerr := tx.Strings(`SELECT id_hash FROM web_sessions ORDER BY created_at`)
		if lerr != nil {
			return fmt.Errorf("list web sessions: %w", lerr)
		}
		for _, id := range ids {
			var (
				principal string
				created   int64
				expiry    int64
				revoked   int64
			)
			if gerr := tx.Get(`SELECT principal, created_at, absolute_expiry, coalesce(revoked_at,0) FROM web_sessions WHERE id_hash = ?1`,
				[]any{id}, &principal, &created, &expiry, &revoked); gerr != nil {
				return fmt.Errorf("read web session: %w", gerr)
			}
			state := "active"
			if revoked != 0 {
				state = "revoked"
			}
			sessionsPrintf(stdout, "%s  %s  created=%s  expires=%s  %s\n",
				id[:12], principal,
				time.Unix(created, 0).UTC().Format(time.RFC3339),
				time.Unix(expiry, 0).UTC().Format(time.RFC3339), state)
			rows++
		}
		return nil
	})
	if err != nil {
		return serveSubFail(stderr, "serve sessions", err)
	}
	if rows == 0 {
		sessionsPrintf(stdout, "no web sessions\n")
	}
	return 0
}

// serveSessionsRevoke revokes one id hash or every session (--all).
func serveSessionsRevoke(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve sessions revoke", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var dataDir, configPath string
	var all bool
	fs.StringVar(&dataDir, "data-dir", "", "data directory")
	fs.StringVar(&configPath, "config", "", "config.toml path")
	fs.BoolVar(&all, "all", false, "revoke every web session")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return 2
	}
	if all == (len(pos) > 0) {
		sessionsPrintf(stderr, "usage: damnbot serve sessions revoke [--all|<id>]\n")
		return 2
	}
	if !all && (len(pos[0]) < 4 || strings.IndexFunc(pos[0], func(r rune) bool {
		return (r < '0' || r > '9') && (r < 'a' || r > 'f')
	}) >= 0) {
		sessionsPrintf(stderr, "serve sessions: id must be a lowercase hex prefix (>= 4)\n")
		return 2
	}
	dir, derr := resolveWebDataDir(dataDir)
	if derr != nil {
		return serveSubFail(stderr, "serve sessions", derr)
	}
	prefix := ""
	if !all {
		prefix = pos[0]
	}
	n, rerr := revokeWebSessions(dir, all, prefix)
	if rerr != nil {
		return serveSubFail(stderr, "serve sessions", rerr)
	}
	sessionsPrintf(stdout, "revoked %d session(s)\n", n)
	return 0
}

// revokeWebSessions stamps revoked_at in the web_sessions table.
func revokeWebSessions(dir string, all bool, prefix string) (int64, error) {
	db, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: dir, Kind: "cli"})
	if err != nil {
		return 0, fmt.Errorf("open ctxdb: %w", err)
	}
	now := time.Now().Unix()
	var n int64
	err = db.Write(context.Background(), func(tx *ctxdb.Tx) error {
		if all {
			affected, xerr := tx.Exec(`UPDATE web_sessions SET revoked_at = ?1 WHERE revoked_at IS NULL`, now)
			n = affected
			if xerr != nil {
				return fmt.Errorf("revoke all web sessions: %w", xerr)
			}
			return nil
		}
		affected, xerr := tx.Exec(`UPDATE web_sessions SET revoked_at = ?1 WHERE id_hash LIKE ?2 AND revoked_at IS NULL`, now, prefix+"%")
		n = affected
		if xerr != nil {
			return fmt.Errorf("revoke web session: %w", xerr)
		}
		return nil
	})
	cerr := db.CloseContext(context.Background())
	if err != nil {
		return 0, fmt.Errorf("write ctxdb: %w", err)
	}
	if cerr != nil {
		return 0, fmt.Errorf("close ctxdb: %w", cerr)
	}
	return n, nil
}

// resolveDataDir resolves --data-dir like the daemon: explicit flag wins,
// otherwise the active profile's data dir from config.
func resolveWebDataDir(dataDir string) (string, error) {
	if dataDir != "" {
		return dataDir, nil
	}
	loader, err := config.New(config.OS())
	if err != nil {
		return "", fmt.Errorf("config deps: %w", err)
	}
	paths, err := loader.Paths(activeProfile())
	if err != nil {
		return "", fmt.Errorf("config paths: %w", err)
	}
	return paths.DataDir, nil
}

// sessionsFail prints one sanitized error and returns the software exit code.
func serveSubFail(stderr io.Writer, name string, err error) int {
	sessionsPrintf(stderr, "%s: %s\n", name, cleanLine(err.Error()))
	return 1
}
