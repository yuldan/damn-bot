//go:build !minimal

package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/mcpservestore"
)

func mcpServeToken(args []string, dataDir string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return mcpUsageErr(stderr, "serve token create|list|revoke expected")
	}
	_, db, code := openServeStore(dataDir, stderr)
	if code != 0 {
		return code
	}
	defer mcpCloseDB(db, stderr)
	store := mcpservestore.Store{DB: db}
	switch args[0] {
	case "create":
		return mcpServeTokenCreate(store, args[1:], stdout, stderr)
	case "list":
		return mcpServeTokenList(store, stdout, stderr)
	case "revoke":
		return mcpServeTokenRevoke(store, args[1:], stdout, stderr)
	}
	return mcpUsageErr(stderr, "serve token: unknown subcommand %q", cleanArg(args[0]))
}

func mcpServeTokenCreate(store mcpservestore.Store, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mcp serve token create", flag.ContinueOnError)
	fs.SetOutput(stderr)
	scope := fs.String("scope", "read", "read, send, or approve")
	identity := fs.String("identity", "", "client identity recorded on approvals")
	if err := fs.Parse(args); err != nil {
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
	row := mcpservestore.Token{
		IDHash: mcpservestore.Hash(raw), Profile: activeProfile(), Identity: *identity,
		Scope: *scope, CreatedAt: time.Now().UnixMilli(),
	}
	if err := store.Create(context.Background(), &row); err != nil {
		return mcpFail(stderr, "serve token create", err)
	}
	sessionsPrintf(stdout, "%s\n", raw) // shown once: stdout only, never config/log
	return 0
}

func mcpServeTokenList(store mcpservestore.Store, stdout, stderr io.Writer) int {
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
}

func mcpServeTokenRevoke(store mcpservestore.Store, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 || len(args[0]) < 8 {
		return mcpUsageErr(stderr, "token revoke <id-prefix>")
	}
	rows, err := store.List(context.Background(), activeProfile())
	if err != nil {
		return mcpFail(stderr, "serve token revoke", err)
	}
	var matches []mcpservestore.Token
	for _, row := range rows {
		if strings.HasPrefix(row.IDHash, args[0]) {
			matches = append(matches, row)
		}
	}
	if len(matches) != 1 {
		return mcpFail(stderr, "serve token revoke", fmt.Errorf("%d tokens match %s", len(matches), cleanArg(args[0])))
	}
	ok, err := store.Revoke(context.Background(), matches[0].IDHash, time.Now().UnixMilli())
	if err != nil {
		return mcpFail(stderr, "serve token revoke", err)
	}
	state := "revoked"
	if !ok {
		state = "already revoked"
	}
	sessionsPrintf(stdout, "%s: %s\n", matches[0].IDHash[:12], state)
	return 0
}

// openServeStore resolves the data dir and opens ctxdb for token management.
func openServeStore(dataDir string, stderr io.Writer) (config.Paths, *ctxdb.Store, int) {
	loader, err := config.New(config.OS())
	if err != nil {
		return config.Paths{}, nil, mcpFail(stderr, "serve token", err)
	}
	paths, err := loader.Paths(activeProfile())
	if err != nil {
		return config.Paths{}, nil, mcpFail(stderr, "serve token", err)
	}
	dir := dataDir
	if dir == "" {
		dir = paths.DataDir
	}
	db, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: dir, Kind: "mcp_serve"})
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
