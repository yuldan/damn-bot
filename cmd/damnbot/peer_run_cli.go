//go:build !minimal

package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"time"

	"github.com/damnbot/damnbot/internal/gwpeer"
	"github.com/damnbot/damnbot/internal/netx"
)

// peerRunState is the 0600 run record `status` and `stop` read. The stop token
// stays in this file and is never printed.
type peerRunState struct {
	ID        string    `json:"id"`
	URL       string    `json:"url"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
	StopToken string    `json:"stop_token"`
}

func peerRun(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("peer run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data-dir", "", "data directory")
	id := fs.String("id", "", "local peer id")
	listen := fs.String("listen", "127.0.0.1:8788", "listen address")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *id == "" {
		peerUsage(stderr)
		return 2
	}
	dir, err := peerDataDir(*dataDir)
	if err != nil {
		sessionsPrintf(stderr, "peer run: %v\n", err)
		return 1
	}
	book, _, statePath := peerPaths(dir)
	peers, err := gwpeer.OpenBook(book)
	if err != nil {
		sessionsPrintf(stderr, "peer run: %v\n", err)
		return 1
	}
	defer peerClose(peers, stderr)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", *listen)
	if err != nil {
		sessionsPrintf(stderr, "peer run: %v\n", err)
		return 1
	}
	state := peerRunState{
		ID: *id, URL: "http://" + ln.Addr().String(), PID: os.Getpid(),
		StartedAt: time.Now().UTC(), StopToken: gwpeer.NewID(),
	}
	if err := writePeerState(statePath, &state); err != nil {
		sessionsPrintf(stderr, "peer run: %v\n", err)
		return 1
	}
	defer removePeerState(statePath, stderr)
	inbox := &peerInbox{path: filepath.Join(dir, "peer", "inbox.jsonl")}
	srv := &gwpeer.Server{
		ID: *id,
		Peers: func(peer string) (gwpeer.Peer, bool) {
			return peers.Get(peer)
		},
		Tokens:    gwpeer.EnvTokens{},
		StopToken: state.StopToken,
		Stop:      cancel,
		Dispatch:  inbox.add,
	}
	sessionsPrintf(stdout, "peer %s listening on %s\n", *id, state.URL)
	if err := srv.Serve(ctx, ln); err != nil {
		sessionsPrintf(stderr, "peer run: %v\n", err)
		return 1
	}
	return 0
}

func peerStatus(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("peer status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data-dir", "", "data directory")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dir, err := peerDataDir(*dataDir)
	if err != nil {
		sessionsPrintf(stderr, "peer status: %v\n", err)
		return 1
	}
	bookPath, keyPath, statePath := peerPaths(dir)
	book, err := gwpeer.OpenBook(bookPath)
	if err != nil {
		sessionsPrintf(stderr, "peer status: %v\n", err)
		return 1
	}
	defer peerClose(book, stderr)
	key, err := gwpeer.LoadOrCreateKey(keyPath)
	if err != nil {
		sessionsPrintf(stderr, "peer status: %v\n", err)
		return 1
	}
	public, ok := key.Public().(ed25519.PublicKey)
	if !ok {
		sessionsPrintf(stderr, "peer status: identity key is not Ed25519\n")
		return 1
	}
	sessionsPrintf(stdout, "fingerprint %s\npeers %d\n", gwpeer.Fingerprint(public), len(book.List()))
	state, err := readPeerState(statePath)
	if err != nil {
		sessionsPrintf(stdout, "running no\n")
		return 0
	}
	sessionsPrintf(stdout, "running yes %s pid %d since %s\n", state.URL, state.PID, state.StartedAt.Format(time.RFC3339))
	return 0
}

func peerStop(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("peer stop", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data-dir", "", "data directory")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dir, err := peerDataDir(*dataDir)
	if err != nil {
		sessionsPrintf(stderr, "peer stop: %v\n", err)
		return 1
	}
	_, _, statePath := peerPaths(dir)
	state, err := readPeerState(statePath)
	if err != nil {
		sessionsPrintf(stderr, "peer stop: not running\n")
		return 1
	}
	if err := stopPeer(context.Background(), &state); err != nil {
		sessionsPrintf(stderr, "peer stop: %v\n", err)
		return 1
	}
	sessionsPrintf(stdout, "stopped %s\n", state.ID)
	return 0
}

func stopPeer(ctx context.Context, state *peerRunState) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, state.URL+"/peer/v1/stop", nil)
	if err != nil {
		return fmt.Errorf("build stop request: %w", err)
	}
	req.Header.Set("X-Damnbot-Stop", state.StopToken)
	resp, err := netx.LoopbackClient().Do(req)
	if err != nil {
		return fmt.Errorf("stop %s: %w", state.ID, err)
	}
	if err := resp.Body.Close(); err != nil {
		return fmt.Errorf("stop %s: %w", state.ID, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("stop %s: status %d", state.ID, resp.StatusCode)
	}
	return nil
}

func readPeerState(path string) (peerRunState, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return peerRunState{}, fmt.Errorf("read state: %w", err)
	}
	data, readErr := root.ReadFile(filepath.Base(path))
	closeErr := root.Close()
	if jerr := errors.Join(readErr, closeErr); jerr != nil {
		return peerRunState{}, fmt.Errorf("read state: %w", jerr)
	}
	var state peerRunState
	if err := json.Unmarshal(data, &state); err != nil {
		return peerRunState{}, fmt.Errorf("decode state: %w", err)
	}
	return state, nil
}

func writePeerState(path string, state *peerRunState) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("open state dir: %w", err)
	}
	data, encErr := json.Marshal(state)
	var writeErr error
	if encErr == nil {
		writeErr = root.WriteFile(filepath.Base(path), data, 0o600)
	}
	closeErr := root.Close()
	if jerr := errors.Join(encErr, writeErr, closeErr); jerr != nil {
		return fmt.Errorf("write state: %w", jerr)
	}
	return nil
}

func removePeerState(path string, stderr io.Writer) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		sessionsPrintf(stderr, "peer: remove state: %v\n", err)
	}
}

// peerInbox appends each accepted DM as one JSON line. The file is the durable
// parking spot a stopped adapter leaves behind, so a wake never loses a
// message that was already acknowledged.
type peerInbox struct {
	mu   sync.Mutex
	path string
}

func (i *peerInbox) add(_ context.Context, env *gwpeer.Envelope) error {
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("peer inbox: encode: %w", err)
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	f, err := os.OpenFile(i.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("peer inbox: open: %w", err)
	}
	_, werr := f.Write(append(data, '\n'))
	if err := errors.Join(werr, f.Close()); err != nil {
		return fmt.Errorf("peer inbox: write: %w", err)
	}
	return nil
}
