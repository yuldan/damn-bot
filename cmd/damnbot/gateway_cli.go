package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"

	configos "github.com/damnbot/damnbot/internal/config"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/gwstore"
	"github.com/damnbot/damnbot/internal/gateway"
)

// gatewayPairTTL and gatewayPairPerHour are SPEC §9.7 / SEC-27: a six-digit
// single-use code valid for ten minutes, at most five codes per source per hour.
const (
	gatewayPairTTL     = 10 * time.Minute
	gatewayPairPerHour = 5
	gatewayCLISource   = "cli"
)

// runGateway implements `damnbot gateway` (SPEC §10, GW-003): owner pairing and
// the identity allowlist. It is the operator surface over gwstore; adapters use
// the same store for /pair.
func runGateway(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		gatewayUsage(stderr)
		return 2
	}
	switch args[0] {
	case "pair":
		return gatewayPair(args[1:], stdout, stderr)
	case "allow":
		return gatewayAllow(args[1:], stdout, stderr)
	case "revoke":
		return gatewayRevoke(args[1:], stdout, stderr)
	case "list":
		return gatewayList(args[1:], stdout, stderr)
	case "status":
		return gatewayStatus(args[1:], stdout, stderr)
	case "platform":
		return gatewayPlatform(args[1:], stdout, stderr)
	case "pairing":
		return gatewayPairing(args[1:], stdout, stderr)
	case "chat":
		return gatewayChat(args[1:], stdout, stderr)
	case "enroll":
		return gatewayEnroll(args[1:], stdout, stderr)
	case "migrate":
		return gatewayMigrate(args[1:], stdout, stderr)
	case "migrate-legacy":
		// Alias of `migrate apply` with the caller's flags (--data-dir) kept:
		// dropping them silently migrated the profile default instead.
		return gatewayMigrate(append([]string{"apply"}, args[1:]...), stdout, stderr)
	}
	sessionsPrintf(stderr, "gateway: unknown subcommand %q\n", cleanArg(args[0]))
	gatewayUsage(stderr)
	return 2
}

func gatewayUsage(w io.Writer) {
	sessionsPrintf(w, "usage: damnbot gateway pair <platform> [--data-dir DIR]\n")
	sessionsPrintf(w, "       damnbot gateway allow|revoke <platform> <id> [--role owner|user] [--data-dir DIR]\n")
	sessionsPrintf(w, "       damnbot gateway list [--platform P] [--data-dir DIR]\n")
	sessionsPrintf(w, "       damnbot gateway status [--data-dir DIR]\n")
	sessionsPrintf(w, "       damnbot gateway platform list|pause|resume <name> [--data-dir DIR]\n")
	sessionsPrintf(w, "       damnbot gateway pairing list [platform] | approve <code> [platform:id] | revoke <platform> <id> | clear-pending [platform] [--data-dir DIR]\n")
	sessionsPrintf(w, "       damnbot gateway chat [--platform telegram] [--chat ID] [--thread ID] [--data-dir DIR]\n")
	sessionsPrintf(w, "       damnbot gateway enroll <platform:id> [--data-dir DIR]\n")
	sessionsPrintf(w, "       damnbot gateway migrate status|plan|apply | migrate-legacy [--data-dir DIR]\n")
	sessionsPrintf(w, "bootstrap: DAMNBOT_GATEWAY_OWNER=platform:id and DAMNBOT_GATEWAY_ALLOW=platform:id,... (or a JSON list)\n")
	sessionsPrintf(w, "missing identities only; the database stays the source of truth.\n")
	sessionsPrintf(w, "chat is a local REPL over a running serve; it never opens the database itself.\n")
}

// gatewayOpen opens the data dir, applies the env bootstrap and returns the
// store. Every subcommand uses this one path so bootstrap is never skipped.
func gatewayOpen(ctx context.Context, dataDir string, stderr io.Writer) (gwstore.Store, func(), error) {
	db, err := openSessionsDB(ctx, dataDir)
	if err != nil {
		return gwstore.Store{}, nil, err
	}
	closeFn := func() { //nolint:contextcheck // ctxdb.Store.Close has no context parameter
		if cerr := db.Close(); cerr != nil {
			sessionsPrintf(stderr, "gateway: close db: %v\n", cerr)
		}
	}
	store := gwstore.Store{DB: db}
	if err := gatewayBootstrap(ctx, store); err != nil {
		closeFn()
		return gwstore.Store{}, nil, err
	}
	return store, closeFn, nil
}

// gatewayBootstrap inserts the env-declared owner and allowlist rows when they
// are missing. It never updates an existing row (DB is authoritative).
func gatewayBootstrap(ctx context.Context, store gwstore.Store) error {
	now := time.Now().UnixMilli()
	for _, spec := range []struct{ name, env, role string }{
		{"DAMNBOT_GATEWAY_OWNER", configos.OS().Getenv("DAMNBOT_GATEWAY_OWNER"), "owner"},
		{"DAMNBOT_GATEWAY_ALLOW", configos.OS().Getenv("DAMNBOT_GATEWAY_ALLOW"), "user"},
	} {
		entries, err := gateway.ParseAllowlist(spec.env)
		if err != nil {
			return fmt.Errorf("%s: %w", spec.name, err)
		}
		for _, e := range entries {
			if _, err := store.EnsureIdentity(ctx, e.Platform, e.ExternalID, spec.role, "platform_authenticated", now); err != nil {
				return fmt.Errorf("%s: %s:%s: %w", spec.name, e.Platform, e.ExternalID, err)
			}
		}
	}
	return nil
}

func gatewayPair(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gateway pair", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data-dir", "", "data directory")
	pos, err := parseInterleaved(fs, args)
	if err != nil || len(pos) != 1 {
		gatewayUsage(stderr)
		return 2
	}
	ctx := context.Background()
	store, closeFn, err := gatewayOpen(ctx, *dataDir, stderr)
	if err != nil {
		sessionsPrintf(stderr, "gateway pair: %s\n", cleanLine(err.Error()))
		return 1
	}
	defer closeFn()
	now := time.Now()
	evicted, notice, cerr := prunePairingPending(ctx, store, pos[0], gatewayCLISource, now)
	if cerr != nil {
		// Fail closed: issuing a code without the SEC-27 bounds would let a
		// spammer fill the pending table again.
		sessionsPrintf(stderr, "gateway pair: %s\n", cleanLine(cerr.Error()))
		return 1
	}
	if evicted > 0 {
		sessionsPrintf(stderr, "gateway pair: %d kode pending lama dihapus\n", evicted)
	}
	if notice {
		notifyPairingOwner(ctx, store, pos[0], now)
		sessionsPrintf(stderr, "gateway pair: pending %s penuh; kode tertua dihapus\n", cleanArg(pos[0]))
	}
	p, err := store.CreatePairing(ctx, pos[0], gatewayCLISource, now.UnixMilli(), gatewayPairTTL, gatewayPairPerHour)
	if err != nil {
		if errors.Is(err, gwstore.ErrPairingQuota) {
			sum := sha256.Sum256([]byte(gatewayCLISource))
			slog.New(slog.NewTextHandler(stderr, nil)).Warn("pairing_lockout",
				"platform", pos[0], "identity_hash", hex.EncodeToString(sum[:]))
		}
		sessionsPrintf(stderr, "gateway pair: %s\n", cleanLine(err.Error()))
		return 1
	}
	if rerr := recordPairingPending(ctx, store, pos[0], gatewayCLISource, p.Code, now); rerr != nil {
		sessionsPrintf(stderr, "gateway pair: %s\n", cleanLine(rerr.Error()))
	}
	sessionsPrintf(stdout, "pairing code %s (role %s, expires in %s)\n", p.Code, p.Role, gatewayPairTTL)
	return 0
}

func gatewayAllow(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gateway allow", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data-dir", "", "data directory")
	role := fs.String("role", "user", "owner|user")
	pos, err := parseInterleaved(fs, args)
	if err != nil || len(pos) != 2 {
		gatewayUsage(stderr)
		return 2
	}
	ctx := context.Background()
	store, closeFn, err := gatewayOpen(ctx, *dataDir, stderr)
	if err != nil {
		sessionsPrintf(stderr, "gateway allow: %s\n", cleanLine(err.Error()))
		return 1
	}
	defer closeFn()
	if err := store.SetIdentity(ctx, pos[0], pos[1], *role, "allowlisted", time.Now().UnixMilli()); err != nil {
		sessionsPrintf(stderr, "gateway allow: %s\n", cleanLine(err.Error()))
		return 1
	}
	sessionsPrintf(stdout, "allowed %s:%s (role %s)\n", pos[0], cleanArg(pos[1]), *role)
	return 0
}

func gatewayRevoke(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gateway revoke", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data-dir", "", "data directory")
	pos, err := parseInterleaved(fs, args)
	if err != nil || len(pos) != 2 {
		gatewayUsage(stderr)
		return 2
	}
	ctx := context.Background()
	store, closeFn, err := gatewayOpen(ctx, *dataDir, stderr)
	if err != nil {
		sessionsPrintf(stderr, "gateway revoke: %s\n", cleanLine(err.Error()))
		return 1
	}
	defer closeFn()
	if err := store.SetStatus(ctx, pos[0], pos[1], "blocked", time.Now().UnixMilli()); err != nil {
		sessionsPrintf(stderr, "gateway revoke: %s\n", cleanLine(err.Error()))
		return 1
	}
	sessionsPrintf(stdout, "blocked %s:%s\n", pos[0], cleanArg(pos[1]))
	return 0
}

func gatewayList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gateway list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data-dir", "", "data directory")
	platform := fs.String("platform", "", "filter by platform")
	_, err := parseInterleaved(fs, args)
	if err != nil {
		gatewayUsage(stderr)
		return 2
	}
	ctx := context.Background()
	store, closeFn, err := gatewayOpen(ctx, *dataDir, stderr)
	if err != nil {
		sessionsPrintf(stderr, "gateway list: %s\n", cleanLine(err.Error()))
		return 1
	}
	defer closeFn()
	ids, err := store.ListIdentities(ctx)
	if err != nil {
		sessionsPrintf(stderr, "gateway list: %s\n", cleanLine(err.Error()))
		return 1
	}
	for _, id := range ids {
		if *platform != "" && id.Platform != *platform {
			continue
		}
		sessionsPrintf(stdout, "%s:%s role=%s status=%s strength=%s\n",
			id.Platform, cleanArg(id.ExternalID), id.Role, id.Status, id.Strength)
	}
	return 0
}

func gatewayStatus(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gateway status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data-dir", "", "data directory")
	_, err := parseInterleaved(fs, args)
	if err != nil {
		gatewayUsage(stderr)
		return 2
	}
	ctx := context.Background()
	store, closeFn, err := gatewayOpen(ctx, *dataDir, stderr)
	if err != nil {
		sessionsPrintf(stderr, "gateway status: %s\n", cleanLine(err.Error()))
		return 1
	}
	defer closeFn()
	st, err := store.Status(ctx)
	if err != nil {
		sessionsPrintf(stderr, "gateway status: %s\n", cleanLine(err.Error()))
		return 1
	}
	sessionsPrintf(stdout, "identities %d (owners %d, allowlisted %d, blocked %d, pending %d)\n",
		st.Identities.Total, st.Identities.Owners, st.Identities.Allowlisted, st.Identities.Blocked, st.Identities.Pending)
	sessionsPrintf(stdout, "inbound queued %d\n", st.Inbound)
	sessionsPrintf(stdout, "deliveries queued=%d sending=%d sent=%d failed=%d\n",
		st.Deliveries["queued"], st.Deliveries["sending"], st.Deliveries["sent"], st.Deliveries["failed"])
	sessionsPrintf(stdout, "leases %d\n", st.Leases)
	return 0
}

// gatewayPlatform is GW-018/SLASH-095-096 from the operator side. A running
// serve owns the adapters; without one there is nothing to pause or resume.
func gatewayPlatform(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gateway platform", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data-dir", "", "data directory")
	pos, err := parseInterleaved(fs, args)
	if err != nil || !validPlatformArgs(pos) {
		gatewayUsage(stderr)
		return 2
	}
	dir, err := platformDataDir(*dataDir)
	if err != nil {
		sessionsPrintf(stderr, "gateway platform: %s\n", cleanLine(err.Error()))
		return 1
	}
	verb := pos[0]
	if !platformLockHeld(dir) {
		if verb == "list" {
			sessionsPrintf(stdout, "serve is not running\n")
			return 0
		}
		sessionsPrintf(stderr, "gateway platform %s: serve is not running\n", verb)
		return 1
	}
	if verb != "list" {
		req := platformRequest{Op: "platform_" + verb, Name: pos[1]}
		if qerr := queuePlatform(dir, &req); qerr != nil {
			sessionsPrintf(stderr, "gateway platform %s: %s\n", verb, cleanLine(qerr.Error()))
			return 1
		}
		sessionsPrintf(stdout, "requested %s %s; check `damnbot gateway platform list`\n", verb, cleanArg(pos[1]))
		return 0
	}
	printPlatforms(stdout, readPlatforms(dir))
	return 0
}

func validPlatformArgs(pos []string) bool {
	if len(pos) == 0 {
		return false
	}
	switch pos[0] {
	case "list":
		return len(pos) == 1
	case "pause", "resume":
		return len(pos) == 2
	default:
		return false
	}
}

func printPlatforms(w io.Writer, list []gateway.AdapterStatus) {
	if len(list) == 0 {
		sessionsPrintf(w, "no gateway platforms\n")
		return
	}
	for _, p := range list {
		if p.Err != "" {
			sessionsPrintf(w, "%s %s (%s)\n", p.Key.String(), p.State, p.Err)
			continue
		}
		sessionsPrintf(w, "%s %s\n", p.Key.String(), p.State)
	}
}

// gatewayPairing is CLI-045: operator control over pending pairing codes.
func gatewayPairing(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gateway pairing", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data-dir", "", "data directory")
	pos, err := parseInterleaved(fs, args)
	if err != nil || len(pos) == 0 {
		gatewayUsage(stderr)
		return 2
	}
	ctx := context.Background()
	store, closeFn, err := gatewayOpen(ctx, *dataDir, stderr)
	if err != nil {
		sessionsPrintf(stderr, "gateway pairing: %s\n", cleanLine(err.Error()))
		return 1
	}
	defer closeFn()
	now := time.Now().UnixMilli()
	switch pos[0] {
	case "list":
		return pairingList(ctx, store, pos, stdout, stderr)
	case "clear-pending":
		return pairingClear(ctx, store, pos, stdout, stderr)
	case "revoke":
		return pairingRevoke(ctx, store, pos, now, stdout, stderr)
	case "approve":
		return pairingApprove(ctx, store, pos, now, stdout, stderr)
	}
	gatewayUsage(stderr)
	return 2
}

func pairingClear(ctx context.Context, store gwstore.Store, pos []string, stdout, stderr io.Writer) int {
	if len(pos) > 2 {
		gatewayUsage(stderr)
		return 2
	}
	platform := ""
	if len(pos) == 2 {
		platform = pos[1]
	}
	n, err := clearPendingPairings(ctx, store, platform)
	if err != nil {
		sessionsPrintf(stderr, "gateway pairing clear-pending: %s\n", cleanLine(err.Error()))
		return 1
	}
	sessionsPrintf(stdout, "cleared %d pending pairing code(s)\n", n)
	return 0
}

func pairingApprove(ctx context.Context, store gwstore.Store, pos []string, now int64, stdout, stderr io.Writer) int {
	if len(pos) < 2 || len(pos) > 3 {
		gatewayUsage(stderr)
		return 2
	}
	identity := configos.OS().Getenv("DAMNBOT_GATEWAY_OWNER")
	if len(pos) == 3 {
		identity = pos[2]
	}
	platform, externalID, ok := strings.Cut(identity, ":")
	if !ok {
		platform, externalID = "telegram", identity
	}
	if externalID == "" {
		sessionsPrintf(stderr, "gateway pairing approve: identity is required (platform:id or DAMNBOT_GATEWAY_OWNER)\n")
		return 1
	}
	id, err := store.RedeemPairing(ctx, platform, pos[1], externalID, "platform_authenticated", now)
	if err != nil {
		sessionsPrintf(stderr, "gateway pairing approve: %s\n", cleanLine(err.Error()))
		return 1
	}
	sessionsPrintf(stdout, "approved %s:%s role=%s\n", platform, cleanArg(externalID), id.Role)
	return 0
}

// clearPendingPairings deletes unconsumed codes via their stored JSON state.
func clearPendingPairings(ctx context.Context, store gwstore.Store, platform string) (int, error) {
	pattern := "pair:%"
	if platform != "" {
		pattern = "pair:" + platform + ":%"
	}
	var n int64
	err := store.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		var xerr error
		n, xerr = tx.Exec(`DELETE FROM module_state WHERE module_id = 'gateway' AND key LIKE ?1
			AND COALESCE(json_extract(value, '$.used'), 0) = 0`, pattern)
		if xerr != nil {
			return fmt.Errorf("clear pairing: %w", xerr)
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("gateway pairing clear: %w", err)
	}
	return int(n), nil
}

// gatewayEnroll allowlists an identity idempotently (CLI-011 enroll).
func gatewayEnroll(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gateway enroll", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data-dir", "", "data directory")
	pos, err := parseInterleaved(fs, args)
	if err != nil || len(pos) != 1 {
		gatewayUsage(stderr)
		return 2
	}
	platform, id, ok := strings.Cut(pos[0], ":")
	if !ok || !gwstore.ValidIdentity(platform) || !gwstore.ValidIdentity(id) {
		sessionsPrintf(stderr, "gateway enroll: %q is not platform:id\n", cleanArg(pos[0]))
		return 2
	}
	ctx := context.Background()
	store, closeFn, err := gatewayOpen(ctx, *dataDir, stderr)
	if err != nil {
		sessionsPrintf(stderr, "gateway enroll: %s\n", cleanLine(err.Error()))
		return 1
	}
	defer closeFn()
	now := time.Now().UnixMilli()
	if _, err := store.EnsureIdentity(ctx, platform, id, "user", "platform_authenticated", now); err != nil {
		sessionsPrintf(stderr, "gateway enroll: %s\n", cleanLine(err.Error()))
		return 1
	}
	if err := store.SetStatus(ctx, platform, id, "allowlisted", now); err != nil {
		sessionsPrintf(stderr, "gateway enroll: %s\n", cleanLine(err.Error()))
		return 1
	}
	sessionsPrintf(stdout, "enrolled %s:%s\n", platform, cleanArg(id))
	return 0
}

// gatewayMigrate reports or applies the embedded ctxdb ladder (CLI-011).
func gatewayMigrate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gateway migrate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data-dir", "", "data directory")
	pos, err := parseInterleaved(fs, args)
	if err != nil || len(pos) != 1 {
		gatewayUsage(stderr)
		return 2
	}
	names, latest, err := ctxdb.MigrationLadder()
	if err != nil {
		sessionsPrintf(stderr, "gateway migrate: %s\n", cleanLine(err.Error()))
		return 1
	}
	switch pos[0] {
	case "apply":
		return migrateApply(*dataDir, latest, stdout, stderr)
	case "status", "plan":
		return migrateReport(pos[0], *dataDir, names, latest, stdout, stderr)
	}
	gatewayUsage(stderr)
	return 2
}

func migrateApply(dataDir string, latest int, stdout, stderr io.Writer) int {
	loader, lerr := configos.New(configos.OS())
	if lerr != nil {
		sessionsPrintf(stderr, "gateway migrate apply: %s\n", cleanLine(lerr.Error()))
		return 1
	}
	dir := dataDir
	if dir == "" {
		paths, perr := loader.Paths(activeProfile())
		if perr != nil {
			sessionsPrintf(stderr, "gateway migrate apply: %s\n", cleanLine(perr.Error()))
			return 1
		}
		dir = paths.DataDir
	}
	db, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: dir, Kind: "cli"})
	if err != nil {
		sessionsPrintf(stderr, "gateway migrate apply: %s\n", cleanLine(err.Error()))
		return 1
	}
	if err := db.Close(); err != nil {
		sessionsPrintf(stderr, "gateway migrate apply: close: %s\n", cleanLine(err.Error()))
		return 1
	}
	sessionsPrintf(stdout, "schema %d (up to date)\n", latest)
	return 0
}

func migrateReport(sub, dataDir string, names []string, latest int, stdout, stderr io.Writer) int {
	loader, lerr := configos.New(configos.OS())
	if lerr != nil {
		sessionsPrintf(stderr, "gateway migrate %s: %s\n", sub, cleanLine(lerr.Error()))
		return 1
	}
	dir := dataDir
	if dir == "" {
		paths, perr := loader.Paths(activeProfile())
		if perr != nil {
			sessionsPrintf(stderr, "gateway migrate %s: %s\n", sub, cleanLine(perr.Error()))
			return 1
		}
		dir = paths.DataDir
	}
	db, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: dir, Kind: "cli", ReadOnly: true})
	if err != nil {
		sessionsPrintf(stderr, "gateway migrate %s: %s\n", sub, cleanLine(err.Error()))
		return 1
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			sessionsPrintf(stderr, "gateway migrate: close: %s\n", cleanLine(cerr.Error()))
		}
	}()
	cur, err := db.SchemaVersion(context.Background())
	if err != nil {
		sessionsPrintf(stderr, "gateway migrate %s: %s\n", sub, cleanLine(err.Error()))
		return 1
	}
	pending := 0
	for _, name := range names {
		if len(name) < 4 {
			continue
		}
		v, serr := strconv.Atoi(name[:4])
		if serr != nil {
			continue
		}
		if v <= cur {
			continue
		}
		pending++
		if sub == "plan" {
			sessionsPrintf(stdout, "%s\n", name)
		}
	}
	if sub == "status" {
		sessionsPrintf(stdout, "schema %d/%d, pending %d\n", cur, latest, pending)
	}
	return 0
}

func pairingList(ctx context.Context, store gwstore.Store, pos []string, stdout, stderr io.Writer) int {
	if len(pos) > 2 {
		gatewayUsage(stderr)
		return 2
	}
	platform := ""
	if len(pos) == 2 {
		platform = pos[1]
	}
	n, err := pendingPairings(ctx, store, platform)
	if err != nil {
		sessionsPrintf(stderr, "gateway pairing list: %s\n", cleanLine(err.Error()))
		return 1
	}
	sessionsPrintf(stdout, "pending %d\n", n)
	return 0
}

func pairingRevoke(ctx context.Context, store gwstore.Store, pos []string, now int64, stdout, stderr io.Writer) int {
	if len(pos) != 3 {
		gatewayUsage(stderr)
		return 2
	}
	if err := store.SetStatus(ctx, pos[1], pos[2], "blocked", now); err != nil {
		sessionsPrintf(stderr, "gateway pairing revoke: %s\n", cleanLine(err.Error()))
		return 1
	}
	sessionsPrintf(stdout, "revoked %s:%s\n", pos[1], cleanArg(pos[2]))
	return 0
}

// pendingPairings counts unconsumed codes via their stored JSON state.
func pendingPairings(ctx context.Context, store gwstore.Store, platform string) (int, error) {
	pattern := "pair:%"
	if platform != "" {
		pattern = "pair:" + platform + ":%"
	}
	var n int64
	err := store.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT count(*) FROM module_state WHERE module_id = 'gateway' AND key LIKE ?1
			AND COALESCE(json_extract(value, '$.used'), 0) = 0`, []any{pattern}, &n)
	})
	if err != nil {
		return 0, fmt.Errorf("pairing list: %w", err)
	}
	return int(n), nil
}
