package webhook

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Subscription errors. Compared with errors.Is.
var (
	ErrSubscription = errors.New("webhook: invalid subscription")
	ErrNotFound     = errors.New("webhook: subscription not found")
)

// Subscription is one dynamic webhook target. Only the secret *name* is
// stored; the value lives in the secret store and is never written here.
type Subscription struct {
	ID        string    `json:"id"`
	Provider  string    `json:"provider"` // "generic" or "msgraph"
	URL       string    `json:"url"`
	Events    []string  `json:"events"`
	SecretRef string    `json:"secret_ref"`
	ExpiresAt time.Time `json:"expires_at,omitzero"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ValidateSubscription rejects a target that the receiver could not call
// safely: plaintext only to loopback, no credentials in the URL, and a named
// secret reference.
func ValidateSubscription(sub *Subscription) error {
	if sub == nil {
		return fmt.Errorf("%w: nil", ErrSubscription)
	}
	switch sub.Provider {
	case "", "generic", "msgraph":
	default:
		return fmt.Errorf("%w: provider %q", ErrSubscription, sub.Provider)
	}
	if err := validateURL(sub.URL); err != nil {
		return err
	}
	if len(sub.Events) == 0 {
		return fmt.Errorf("%w: no events", ErrSubscription)
	}
	for _, event := range sub.Events {
		if !eventName.MatchString(event) {
			return fmt.Errorf("%w: event %q", ErrSubscription, event)
		}
	}
	if !ValidID(sub.SecretRef) {
		return fmt.Errorf("%w: secret reference", ErrSubscription)
	}
	return nil
}

func validateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: url: %w", ErrSubscription, err)
	}
	if u.Host == "" || u.User != nil {
		return fmt.Errorf("%w: url host", ErrSubscription)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(u.Hostname()) {
			return nil
		}
	}
	return fmt.Errorf("%w: url must be https (or loopback http)", ErrSubscription)
}

func isLoopbackHost(host string) bool {
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// Store is the file-backed subscription table. It is safe for concurrent use
// and writes atomically (0600, fsync, rename).
type Store struct {
	mu   sync.Mutex
	path string
	now  func() time.Time
	subs []Subscription
}

type storeFile struct {
	Version       int            `json:"version"`
	Subscriptions []Subscription `json:"subscriptions"`
}

// OpenStore loads path, or starts empty when the file does not exist.
func OpenStore(path string, now func() time.Time) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("%w: empty store path", ErrSubscription)
	}
	if now == nil {
		now = time.Now
	}
	s := &Store{path: path, now: now}
	data, err := os.ReadFile(path) // #nosec G304 -- operator-configured subscription file, not request input.
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, fmt.Errorf("webhook: read store: %w", err)
	}
	var file storeFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("webhook: decode store: %w", err)
	}
	s.subs = file.Subscriptions
	return s, nil
}

// Add inserts sub, or returns the existing row when provider, URL, events and
// secret reference are identical (idempotent subscribe).
func (s *Store) Add(sub *Subscription) (Subscription, bool, error) {
	if err := ValidateSubscription(sub); err != nil {
		return Subscription{}, false, err
	}
	if sub.Provider == "" {
		sub.Provider = "generic"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.subs {
		if s.sameTargetLocked(&s.subs[i], sub) {
			return cloneSub(&s.subs[i]), false, nil
		}
	}
	if sub.ID == "" {
		id, err := newID()
		if err != nil {
			return Subscription{}, false, err
		}
		sub.ID = id
	}
	now := s.now().UTC()
	sub.CreatedAt, sub.UpdatedAt = now, now
	s.subs = append(s.subs, cloneSub(sub))
	if err := s.saveLocked(); err != nil {
		s.subs = s.subs[:len(s.subs)-1]
		return Subscription{}, false, err
	}
	return cloneSub(sub), true, nil
}

func (s *Store) sameTargetLocked(existing, candidate *Subscription) bool {
	return existing.Provider == candidate.Provider &&
		existing.URL == candidate.URL &&
		existing.SecretRef == candidate.SecretRef &&
		slices.Equal(existing.Events, candidate.Events)
}

// List returns a deep copy of every row in insertion order.
func (s *Store) List() []Subscription {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Subscription, len(s.subs))
	for i := range s.subs {
		out[i] = cloneSub(&s.subs[i])
	}
	return out
}

// Get returns one row by id.
func (s *Store) Get(id string) (Subscription, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.subs {
		if s.subs[i].ID == id {
			return cloneSub(&s.subs[i]), true
		}
	}
	return Subscription{}, false
}

func cloneSub(sub *Subscription) Subscription {
	out := *sub
	out.Events = slices.Clone(sub.Events)
	return out
}

// Remove deletes one row. Removing an unknown id is ErrNotFound.
func (s *Store) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.subs {
		if s.subs[i].ID != id {
			continue
		}
		removed := s.subs[i]
		s.subs = slices.Delete(s.subs, i, i+1)
		if err := s.saveLocked(); err != nil {
			s.subs = slices.Insert(s.subs, i, removed)
			return err
		}
		return nil
	}
	return fmt.Errorf("%w: %s", ErrNotFound, id)
}

func (s *Store) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("webhook: store dir: %w", err)
	}
	data, err := json.MarshalIndent(storeFile{Version: 1, Subscriptions: s.subs}, "", "  ")
	if err != nil {
		return fmt.Errorf("webhook: encode store: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".subscriptions-*")
	if err != nil {
		return fmt.Errorf("webhook: store temp: %w", err)
	}
	name := tmp.Name()
	if err := tmp.Chmod(0o600); err != nil {
		return discardTemp(tmp, name, fmt.Errorf("webhook: store mode: %w", err))
	}
	if _, err := tmp.Write(data); err != nil {
		return discardTemp(tmp, name, fmt.Errorf("webhook: store write: %w", err))
	}
	if err := tmp.Sync(); err != nil {
		return discardTemp(tmp, name, fmt.Errorf("webhook: store sync: %w", err))
	}
	if err := tmp.Close(); err != nil {
		return discardTemp(nil, name, fmt.Errorf("webhook: store close: %w", err))
	}
	if err := os.Rename(name, s.path); err != nil {
		return discardTemp(nil, name, fmt.Errorf("webhook: store rename: %w", err))
	}
	return nil
}

// discardTemp removes the staging file and joins any cleanup failure into the
// primary error, so a failed write never leaves a second surprise.
func discardTemp(tmp *os.File, name string, primary error) error {
	var closeErr error
	if tmp != nil {
		closeErr = tmp.Close()
	}
	removeErr := os.Remove(name)
	var errs []error
	if closeErr != nil {
		errs = append(errs, closeErr)
	}
	if removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
		errs = append(errs, removeErr)
	}
	return errors.Join(append([]error{primary}, errs...)...)
}

func newID() (string, error) {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("webhook: id: %w", err)
	}
	return "wh_" + strings.ToLower(hex.EncodeToString(raw[:])), nil
}
