//go:build !minimal

package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/mcpservestore"
)

func mcpServeToken(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return mcpUsageErr(stderr, "serve token create|list|revoke expected")
	}
	_, db, code := openServeStore(stderr)
	if code != 0 {
		return code
	}
	defer mcpCloseDB(db, stderr)
	store := mcpservestore.Store{DB: db}
	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("mcp serve token create", flag.ContinueOnError)
		fs.SetOutput(stderr)
		scope := fs.String("scope", "read", "read, send, or approve")
		identity := fs.String("identity", "", "client identity recorded on approvals")
		asJSON := fs.Bool("json", false, "machine-readable one-time token")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		if fs.NArg() != 0 {
			return mcpUsageErr(stderr, "token create takes no arguments")
		}
		if *identity == "" || len(*identity) > 128 || strings.ContainsAny(*identity, "\x00\r\n") {
			return mcpUsageErr(stderr, "--identity is required")
		}
		if *scope != mcpservestore.ScopeRead && *scope != mcpservestore.ScopeSend && *scope != mcpservestore.ScopeApprove {
			return mcpUsageErr(stderr, "--scope must be read, send, or approve")
		}
		raw, err := newServeToken()
		if err != nil {
			return mcpFail(stderr, "serve token create", err)
		}
		row := mcpservestore.Token{IDHash: mcpservestore.Hash(raw), Profile: activeProfile(), Identity: *identity,
			Scope: *scope, CreatedAt: time.Now().UnixMilli()}
		if err := store.Create(context.Background(), row); err != nil {
			return mcpFail(stderr, "serve token create", err)
		}
		if *asJSON {
			out, _ := json.Marshal(map[string]string{"token": raw, "scope": *scope, "identity": *identity})
			return say(stdout, "%s\n", out)
		}
		sessionsPrintf(stdout, "token (shown once): %s\n", raw)
		sessionsPrintf(stdout, "scope=%s identity=%s\n", *scope, *identity)
		return 0
	case "list":
		rows, err := store.List(context.Background(), activeProfile())
		if err != nil {
			return mcpFail(stderr, "serve token list", err)
		}
		for _, row := range rows {
			state := "active"
			if row.RevokedAt > 0 {
				state = "revoked"
			}
			sessionsPrintf(stdout, "%s\t%s\t%s\t%s\t%s\n", row.IDHash[:12], row.Identity, row.Scope, state,
				time.UnixMilli(row.CreatedAt).UTC().Format(time.RFC3339))
		}
		return 0
	case "revoke":
		if len(args) != 2 || len(args[1]) < 8 {
			return mcpUsageErr(stderr, "token revoke <id-prefix>")
		}
		rows, err := store.List(context.Background(), activeProfile())
		if err != nil {
			return mcpFail(stderr, "serve token revoke", err)
		}
		var matches []mcpservestore.Token
		for _, row := range rows {
			if strings.HasPrefix(row.IDHash, args[1]) {
				matches = append(matches, row)
			}
		}
		if len(matches) != 1 {
			return mcpFail(stderr, "serve token revoke", fmt.Errorf("%d tokens match %s", len(matches), cleanArg(args[1])))
		}
		ok, err := store.Revoke(context.Background(), matches[0].IDHash, time.Now().UnixMilli())
		if err != nil {
			return mcpFail(stderr, "serve token revoke", err)
		}
		if !ok {
			sessionsPrintf(stdout, "%s: already revoked\n", matches[0].IDHash[:12])
			return 0
		}
		sessionsPrintf(stdout, "%s: revoked\n", matches[0].IDHash[:12])
		return 0
	}
	return mcpUsageErr(stderr, "serve token: unknown subcommand %q", cleanArg(args[0]))
}

// openServeStore resolves the profile data dir and opens ctxdb for token
// management.
func openServeStore(stderr io.Writer) (config.Paths, *ctxdb.Store, int) {
	loader, err := config.New(config.OS())
	if err != nil {
		return config.Paths{}, nil, mcpFail(stderr, "serve token", err)
	}
	paths, err := loader.Paths(activeProfile())
	if err != nil {
		return config.Paths{}, nil, mcpFail(stderr, "serve token", err)
	}
	db, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: paths.DataDir, Kind: "mcp_serve"})
	if err != nil {
		return config.Paths{}, nil, mcpFail(stderr, "serve token", err)
	}
	return paths, db, 0
}

func newServeToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
