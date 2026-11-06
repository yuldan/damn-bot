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
	"path/filepath"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/gwpeer"
	"github.com/damnbot/damnbot/internal/netx"
)

// runPeer implements `damnbot peer` (CLI-027): add, list, remove, dm, run,
// status, stop. The peer book and identity key live under the profile data
// directory; tokens are resolved from the secret source at send time and are
// never printed.
func runPeer(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		peerUsage(stderr)
		return 2
	}
	switch args[0] {
	case "add":
		return peerAdd(args[1:], stdout, stderr)
	case "list":
		return peerList(args[1:], stdout, stderr)
	case "remove":
		return peerRemove(args[1:], stdout, stderr)
	case "dm":
		return peerDM(args[1:], stdout, stderr)
	}
	sessionsPrintf(stderr, "peer: unknown subcommand %q\n", cleanArg(args[0]))
	peerUsage(stderr)
	return 2
}

func peerUsage(w io.Writer) {
	sessionsPrintf(w, "usage: damnbot peer add --id ID --url URL --token-ref ENV --pubkey BASE64 [--data-dir DIR]\n")
	sessionsPrintf(w, "       damnbot peer list [--data-dir DIR]\n")
	sessionsPrintf(w, "       damnbot peer remove ID [--data-dir DIR]\n")
	sessionsPrintf(w, "       damnbot peer dm --id LOCAL --peer ID --text TEXT [--chat ID] [--data-dir DIR]\n")
	sessionsPrintf(w, "tokens: the referenced environment variable holds the peer bearer token.\n")
}

func peerDataDir(flagValue string) (string, error) {
	if flagValue != "" {
		return resolveDataDir(flagValue)
	}
	loader, err := config.New(config.OS())
	if err != nil {
		return "", fmt.Errorf("config: %w", err)
	}
	paths, err := loader.Paths(activeProfile())
	if err != nil {
		return "", fmt.Errorf("config paths: %w", err)
	}
	return paths.DataDir, nil
}

func peerPaths(dataDir string) (string, string, string) {
	root := filepath.Join(dataDir, "peer")
	return filepath.Join(root, "peers.json"), filepath.Join(root, "identity.key"), filepath.Join(root, "run.json")
}

func peerBook(dataDir string) (*gwpeer.Book, string, error) {
	bookPath, keyPath, _ := peerPaths(dataDir)
	book, err := gwpeer.OpenBook(bookPath)
	if err != nil {
		return nil, "", fmt.Errorf("gwpeer: open peer book: %w", err)
	}
	return book, keyPath, nil
}

func peerAdd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("peer add", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data-dir", "", "data directory")
	id := fs.String("id", "", "peer id")
	rawURL := fs.String("url", "", "peer base url")
	tokenRef := fs.String("token-ref", "", "environment variable holding the bearer token")
	pubKey := fs.String("pubkey", "", "peer Ed25519 public key (base64)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		peerUsage(stderr)
		return 2
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(*pubKey))
	if err != nil {
		sessionsPrintf(stderr, "peer add: pubkey: %v\n", err)
		return 2
	}
	dir, err := peerDataDir(*dataDir)
	if err != nil {
		sessionsPrintf(stderr, "peer add: %v\n", err)
		return 1
	}
	book, _, err := peerBook(dir)
	if err != nil {
		sessionsPrintf(stderr, "peer add: %v\n", err)
		return 1
	}
	defer peerClose(book, stderr)
	p := gwpeer.Peer{ID: *id, URL: *rawURL, TokenRef: *tokenRef, PublicKey: ed25519.PublicKey(key)}
	if err := book.Add(&p, time.Now()); err != nil {
		sessionsPrintf(stderr, "peer add: %v\n", err)
		return 1
	}
	sessionsPrintf(stdout, "added %s %s\n", p.ID, p.URL)
	return 0
}

func peerList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("peer list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data-dir", "", "data directory")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dir, err := peerDataDir(*dataDir)
	if err != nil {
		sessionsPrintf(stderr, "peer list: %v\n", err)
		return 1
	}
	book, _, err := peerBook(dir)
	if err != nil {
		sessionsPrintf(stderr, "peer list: %v\n", err)
		return 1
	}
	defer peerClose(book, stderr)
	for _, p := range book.List() {
		sessionsPrintf(stdout, "%s\t%s\t%s\n", p.ID, p.URL, gwpeer.Fingerprint(p.PublicKey))
	}
	return 0
}

func peerRemove(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("peer remove", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data-dir", "", "data directory")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		peerUsage(stderr)
		return 2
	}
	dir, err := peerDataDir(*dataDir)
	if err != nil {
		sessionsPrintf(stderr, "peer remove: %v\n", err)
		return 1
	}
	book, _, err := peerBook(dir)
	if err != nil {
		sessionsPrintf(stderr, "peer remove: %v\n", err)
		return 1
	}
	defer peerClose(book, stderr)
	if err := book.Remove(fs.Arg(0)); err != nil {
		sessionsPrintf(stderr, "peer remove: %v\n", err)
		return 1
	}
	sessionsPrintf(stdout, "removed %s\n", fs.Arg(0))
	return 0
}

func peerDM(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("peer dm", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data-dir", "", "data directory")
	local := fs.String("id", "", "local peer id")
	target := fs.String("peer", "", "registered peer id")
	chat := fs.String("chat", "dm", "chat id")
	text := fs.String("text", "", "message text")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dir, err := peerDataDir(*dataDir)
	if err != nil {
		sessionsPrintf(stderr, "peer dm: %v\n", err)
		return 1
	}
	book, keyPath, err := peerBook(dir)
	if err != nil {
		sessionsPrintf(stderr, "peer dm: %v\n", err)
		return 1
	}
	defer peerClose(book, stderr)
	peer, ok := book.Get(*target)
	if !ok {
		sessionsPrintf(stderr, "peer dm: %v: %s\n", gwpeer.ErrPeerNotFound, cleanArg(*target))
		return 1
	}
	key, err := gwpeer.LoadOrCreateKey(keyPath)
	if err != nil {
		sessionsPrintf(stderr, "peer dm: %v\n", err)
		return 1
	}
	loader, err := config.New(config.OS())
	if err != nil {
		sessionsPrintf(stderr, "peer dm: config: %v\n", err)
		return 1
	}
	cfg, err := loader.LoadProfile(activeProfile())
	if err != nil && !errors.Is(err, config.ErrRead) {
		sessionsPrintf(stderr, "peer dm: config: %v\n", err)
		return 1
	}
	client := &gwpeer.Client{
		LocalID: *local, Key: key, Tokens: gwpeer.EnvTokens{},
		HTTP: peerDMClient(netx.New(netx.Config{
			Offline: cfg.Network.Offline, NoBackground: !cfg.Network.Background,
		})),
	}
	env, err := client.Send(context.Background(), &peer, *chat, *text)
	if gwpeer.IsDuplicate(err) {
		sessionsPrintf(stdout, "delivered %s (already)\n", env.ID)
		return 0
	}
	if err != nil {
		sessionsPrintf(stderr, "peer dm: %v\n", err)
		return 1
	}
	sessionsPrintf(stdout, "sent %s\n", env.ID)
	return 0
}

func peerClose(book *gwpeer.Book, stderr io.Writer) {
	if err := book.Close(); err != nil {
		sessionsPrintf(stderr, "peer: close book: %v\n", err)
	}
}

// peerDMClient is the egress seam for `damnbot peer dm` (GW-013): a gateway
// client labelled peer-dm, so the registry and `doctor --network` cover it.
func peerDMClient(n *netx.Network) *netx.Client {
	return n.Client("peer-dm", netx.Gateway)
}
