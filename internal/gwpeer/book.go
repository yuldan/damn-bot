package gwpeer

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Book is the local registry of remote peers, persisted as one JSON file.
type Book struct {
	mu    sync.Mutex
	root  *os.Root
	name  string
	peers []Peer
}

// OpenBook loads the peer book at path. A missing file is an empty book.
func OpenBook(path string) (*Book, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("gwpeer: create peer directory: %w", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("gwpeer: open peer directory: %w", err)
	}
	b := &Book{root: root, name: filepath.Base(path)}
	if err := b.load(); err != nil {
		if cerr := root.Close(); cerr != nil {
			return nil, errors.Join(err, fmt.Errorf("gwpeer: close peer directory: %w", cerr))
		}
		return nil, err
	}
	return b, nil
}

// Close releases the directory handle. It does not save.
func (b *Book) Close() error {
	if b.root == nil {
		return nil
	}
	if err := b.root.Close(); err != nil {
		return fmt.Errorf("gwpeer: close peer book: %w", err)
	}
	return nil
}

func (b *Book) load() error {
	data, err := b.root.ReadFile(b.name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("gwpeer: read peer book: %w", err)
	}
	var peers []Peer
	if err := json.Unmarshal(data, &peers); err != nil {
		return fmt.Errorf("%w: decode peer book: %w", ErrInvalidPeer, err)
	}
	for i := range peers {
		if err := peers[i].Validate(); err != nil {
			return fmt.Errorf("gwpeer: peer book entry %d: %w", i, err)
		}
	}
	b.peers = peers
	return nil
}

// Add registers p and persists the book. A duplicate id is rejected so a
// stale entry can never silently redirect a peer.
func (b *Book) Add(input *Peer, now time.Time) error {
	p := *input
	if err := p.Validate(); err != nil {
		return err
	}
	if p.AddedAt.IsZero() {
		p.AddedAt = now.UTC()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if slices.ContainsFunc(b.peers, func(q Peer) bool { return q.ID == p.ID }) {
		return fmt.Errorf("%w: %s", ErrPeerExists, p.ID)
	}
	b.peers = append(b.peers, p)
	if err := b.saveLocked(); err != nil {
		b.peers = b.peers[:len(b.peers)-1]
		return err
	}
	return nil
}

// Remove deletes id and persists the book.
func (b *Book) Remove(id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	i := slices.IndexFunc(b.peers, func(p Peer) bool { return p.ID == id })
	if i < 0 {
		return fmt.Errorf("%w: %s", ErrPeerNotFound, id)
	}
	b.peers = slices.Delete(b.peers, i, i+1)
	return b.saveLocked()
}

// List returns a copy of the registered peers, ordered by id.
func (b *Book) List() []Peer {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := slices.Clone(b.peers)
	slices.SortFunc(out, func(a, c Peer) int { return strings.Compare(a.ID, c.ID) })
	return out
}

// Get returns the peer with id.
func (b *Book) Get(id string) (Peer, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	i := slices.IndexFunc(b.peers, func(p Peer) bool { return p.ID == id })
	if i < 0 {
		return Peer{}, false
	}
	return b.peers[i], true
}

func (b *Book) saveLocked() error {
	data, err := json.MarshalIndent(b.peers, "", "  ")
	if err != nil {
		return fmt.Errorf("gwpeer: encode peer book: %w", err)
	}
	if err := b.root.WriteFile(b.name, data, 0o600); err != nil {
		return fmt.Errorf("gwpeer: write peer book: %w", err)
	}
	return nil
}
