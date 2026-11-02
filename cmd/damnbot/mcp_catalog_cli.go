//go:build !minimal

package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/mcp/catalog"
	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/update"
)

// mcpCatalog verifies one signed provider/MCP catalog manifest with the
// operator key from [update] catalog_public_key (GAP-SEC-20). It accepts a
// local file or a live URL; the URL fetch follows at most three same-host
// redirects. The last accepted serial is persisted before the next run.
func mcpCatalog(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mcp catalog", flag.ContinueOnError)
	fs.SetOutput(stderr)
	keyFlag := fs.String("key", "", "base64 ed25519 public key (overrides config)")
	urlFlag := fs.String("url", "", "fetch the manifest from this URL")
	seqDir := fs.String("seq-dir", "", "serial state directory (default <data>/catalog-seq)")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return 2
	}
	if (*urlFlag == "") == (len(pos) == 0) {
		return mcpUsageErr(stderr, "catalog needs exactly one manifest file or --url")
	}
	_, cfg, paths, err := openMCPConfig()
	if err != nil {
		return mcpFail(stderr, "catalog", err)
	}
	key, err := catalogKey(*keyFlag, &cfg)
	if err != nil {
		return mcpFail(stderr, "catalog", err)
	}
	dir := *seqDir
	if dir == "" {
		dir = filepath.Join(paths.DataDir, "catalog-seq")
	}
	store := catalog.FileSeqStore{Dir: dir}
	var m catalog.Manifest
	if *urlFlag != "" {
		client := netx.New(netx.Config{}).Client("update", netx.Background).WithSameHostRedirects(3)
		m, err = fetchCatalog(*urlFlag, key, client, store)
	} else {
		m, err = verifyCatalogFile(pos[0], key, store)
	}
	if err != nil {
		return mcpFail(stderr, "catalog", err)
	}
	sessionsPrintf(stdout, "catalog %s/%s: version=%s serial=%d entries=%d\n", m.Kind, m.Name, m.Version, m.Serial, len(m.Entries))
	return 0
}

func verifyCatalogFile(path string, key ed25519.PublicKey, store catalog.FileSeqStore) (catalog.Manifest, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return catalog.Manifest{}, fmt.Errorf("open manifest dir: %w", err)
	}
	defer func() {
		if cerr := root.Close(); cerr != nil {
			return
		}
	}()
	f, err := root.Open(filepath.Base(path))
	if err != nil {
		return catalog.Manifest{}, fmt.Errorf("read manifest: %w", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			return
		}
	}()
	raw, err := io.ReadAll(io.LimitReader(f, catalog.MaxManifestBytes+1))
	if err != nil {
		return catalog.Manifest{}, fmt.Errorf("read manifest: %w", err)
	}
	if len(raw) > catalog.MaxManifestBytes {
		return catalog.Manifest{}, errors.New("manifest exceeds 16 MiB")
	}
	m, verr := catalog.VerifyWithStore(raw, key, store)
	if verr != nil {
		return catalog.Manifest{}, fmt.Errorf("verify manifest: %w", verr)
	}
	return m, nil
}

func fetchCatalog(url string, key ed25519.PublicKey, client *netx.Client, store catalog.FileSeqStore) (catalog.Manifest, error) {
	m, err := catalog.Fetch(context.Background(), client, url, key, 0)
	if err != nil {
		return catalog.Manifest{}, fmt.Errorf("fetch manifest: %w", err)
	}
	last, err := store.Last(m.Kind, m.Name)
	if err != nil {
		return catalog.Manifest{}, fmt.Errorf("read serial: %w", err)
	}
	if m.Serial <= last {
		return catalog.Manifest{}, fmt.Errorf("%w: serial %d <= %d", catalog.ErrRollback, m.Serial, last)
	}
	if err := store.Save(m.Kind, m.Name, m.Serial); err != nil {
		return catalog.Manifest{}, fmt.Errorf("save serial: %w", err)
	}
	return m, nil
}

// catalogKey resolves the verification key: the explicit flag wins, else the
// configured [update] catalog_public_key. Missing key fails closed.
func catalogKey(flagValue string, cfg *config.Config) (ed25519.PublicKey, error) {
	text := strings.TrimSpace(flagValue)
	if text == "" && cfg != nil {
		text = strings.TrimSpace(cfg.Update.CatalogPublicKey)
	}
	if text == "" {
		return nil, errors.New("no catalog public key: set [update] catalog_public_key or --key")
	}
	if err := update.ValidateCatalogPublicKey(text); err != nil {
		return nil, fmt.Errorf("catalog public key: %w", err)
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(text)
	if err != nil {
		return nil, fmt.Errorf("catalog public key: %w", err)
	}
	return ed25519.PublicKey(raw), nil
}
