package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

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
	sessionsPrintf(w, "bootstrap: DAMNBOT_GATEWAY_OWNER=platform:id and DAMNBOT_GATEWAY_ALLOW=platform:id,... (or a JSON list)\n")
	sessionsPrintf(w, "missing identities only; the database stays the source of truth.\n")
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
		{"DAMNBOT_GATEWAY_OWNER", os.Getenv("DAMNBOT_GATEWAY_OWNER"), "owner"},
		{"DAMNBOT_GATEWAY_ALLOW", os.Getenv("DAMNBOT_GATEWAY_ALLOW"), "user"},
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
	p, err := store.CreatePairing(ctx, pos[0], gatewayCLISource, time.Now().UnixMilli(), gatewayPairTTL, gatewayPairPerHour)
	if err != nil {
		sessionsPrintf(stderr, "gateway pair: %s\n", cleanLine(err.Error()))
		return 1
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
