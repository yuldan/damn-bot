//go:build !minimal

package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/damnbot/damnbot/internal/mcp/catalog"
)

// mcpCatalog verifies one signed provider/MCP catalog manifest with the
// operator key from [update] catalog_public_key (GAP-SEC-20). It never lists
// an entry before the signature and serial checks pass.
func mcpCatalog(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mcp catalog", flag.ContinueOnError)
	fs.SetOutput(stderr)
	keyFlag := fs.String("key", "", "base64 ed25519 public key (overrides config)")
	last := fs.Int64("last-serial", 0, "last accepted serial; lower/equal is a rollback")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return 2
	}
	if len(pos) != 1 {
		return mcpUsageErr(stderr, "catalog needs exactly one manifest file")
	}
	raw, err := os.ReadFile(pos[0])
	if err != nil {
		return mcpFail(stderr, "catalog", fmt.Errorf("read manifest: %w", err))
	}
	if len(raw) > 16<<20 {
		return mcpFail(stderr, "catalog", errors.New("manifest exceeds 16 MiB"))
	}
	key, err := catalogKey(*keyFlag)
	if err != nil {
		return mcpFail(stderr, "catalog", err)
	}
	m, err := catalog.Verify(raw, key, *last)
	if err != nil {
		return mcpFail(stderr, "catalog", err)
	}
	sessionsPrintf(stdout, "catalog %s/%s: version=%s serial=%d entries=%d\n", m.Kind, m.Name, m.Version, m.Serial, len(m.Entries))
	return 0
}

// catalogKey resolves the verification key: the explicit flag wins, else the
// configured [update] catalog_public_key. Missing key fails closed.
func catalogKey(flagValue string) (ed25519.PublicKey, error) {
	text := strings.TrimSpace(flagValue)
	if text == "" {
		_, cfg, _, err := openMCPConfig()
		if err != nil {
			return nil, err
		}
		text = strings.TrimSpace(cfg.Update.CatalogPublicKey)
	}
	if text == "" {
		return nil, errors.New("no catalog public key: set [update] catalog_public_key or --key")
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(text)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("catalog public key must be base64 ed25519 (%d bytes)", ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(raw), nil
}
