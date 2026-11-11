package connectors

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// HostedAccount is one account of a hosted connector app (PLUG-035). TokenRef
// is a keychain reference, never the OAuth token value.
type HostedAccount struct {
	Connector string `json:"connector"`
	ID        string `json:"id"`
	Label     string `json:"label"`
	TokenRef  string `json:"token_ref"`
}

// ErrConnection rejects a malformed hosted connector account.
var ErrConnection = errors.New("connectors: hosted connection")

// AccountStore is the management surface manage_connections reads; it holds references only.
type AccountStore interface {
	Add(connector, id, label, tokenRef string) (HostedAccount, error)
	Rename(connector, id, label string) error
	Remove(connector, id string) error
	List(connector string) []HostedAccount
}

// Connections is the file-backed hosted account store. An empty path keeps
// the store in memory (tests and builds without a data directory). It holds
// references only; no token value is accepted or returned.
type Connections struct {
	mu       sync.Mutex
	accounts map[string]HostedAccount
	path     string
}

// NewConnections returns an empty in-memory account store.
func NewConnections() *Connections {
	return &Connections{accounts: map[string]HostedAccount{}}
}

// OpenConnections loads the store at path. A missing file is an empty store;
// a malformed file is an error, never a silent reset.
func OpenConnections(path string) (*Connections, error) {
	c := NewConnections()
	c.path = path
	b, err := os.ReadFile(path) // #nosec G304 -- operator data directory
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return nil, fmt.Errorf("connectors: read hosted accounts: %w", err)
	}
	var accounts []HostedAccount
	if err := json.Unmarshal(b, &accounts); err != nil {
		return nil, fmt.Errorf("connectors: parse hosted accounts: %w", err)
	}
	for _, acc := range accounts {
		if _, err := c.validate(acc.Connector, acc.ID, acc.Label, acc.TokenRef); err != nil {
			return nil, fmt.Errorf("connectors: hosted accounts: %w", err)
		}
		c.accounts[acc.Connector+"/"+acc.ID] = acc
	}
	return c, nil
}

func connectorKnown(name string) bool {
	for _, d := range Builtin() {
		if d.Name == name {
			return true
		}
	}
	return false
}

func (c *Connections) validate(connector, id, label, tokenRef string) (HostedAccount, error) {
	if !connectorKnown(connector) {
		return HostedAccount{}, fmt.Errorf("%w: unknown connector %q", ErrConnection, connector)
	}
	if id == "" || label == "" || !strings.HasPrefix(tokenRef, "keychain:") || len(tokenRef) <= len("keychain:") {
		return HostedAccount{}, fmt.Errorf("%w: account %q needs id, label and a keychain reference", ErrConnection, id)
	}
	return HostedAccount{Connector: connector, ID: id, Label: label, TokenRef: tokenRef}, nil
}

// Add registers one account. The connector must be in the closed connectors
// registry and tokenRef must be a keychain: reference.
func (c *Connections) Add(connector, id, label, tokenRef string) (HostedAccount, error) {
	acc, err := c.validate(connector, id, label, tokenRef)
	if err != nil {
		return HostedAccount{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key := connector + "/" + id
	if _, exists := c.accounts[key]; exists {
		return HostedAccount{}, fmt.Errorf("%w: account %q exists", ErrConnection, id)
	}
	c.accounts[key] = acc
	return acc, c.saveLocked()
}

// Rename changes the display label of one account.
func (c *Connections) Rename(connector, id, label string) error {
	if label == "" {
		return fmt.Errorf("%w: empty label", ErrConnection)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key := connector + "/" + id
	acc, ok := c.accounts[key]
	if !ok {
		return fmt.Errorf("%w: account %q", ErrConnection, id)
	}
	acc.Label = label
	c.accounts[key] = acc
	return c.saveLocked()
}

// Remove deletes one account.
func (c *Connections) Remove(connector, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := connector + "/" + id
	if _, ok := c.accounts[key]; !ok {
		return fmt.Errorf("%w: account %q", ErrConnection, id)
	}
	delete(c.accounts, key)
	return c.saveLocked()
}

// List returns the accounts of one connector sorted by id; empty connector
// lists all accounts. Values are references only.
func (c *Connections) List(connector string) []HostedAccount {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.listLocked(connector)
}

func (c *Connections) listLocked(connector string) []HostedAccount {
	out := make([]HostedAccount, 0, len(c.accounts))
	for _, acc := range c.accounts {
		if connector == "" || acc.Connector == connector {
			out = append(out, acc)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Connector != out[j].Connector {
			return out[i].Connector < out[j].Connector
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// saveLocked persists the store when it has a path. The file is written 0600
// and replaced through a temporary file so a crash never leaves half JSON.
func (c *Connections) saveLocked() (err error) {
	if c.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(c.listLocked(""), "", "  ")
	if err != nil {
		return fmt.Errorf("connectors: encode hosted accounts: %w", err)
	}
	b = append(b, '\n')
	dir := filepath.Dir(c.path)
	if merr := os.MkdirAll(dir, 0o700); merr != nil {
		return fmt.Errorf("connectors: mkdir hosted accounts: %w", merr)
	}
	tmp, err := os.CreateTemp(dir, ".hosted-*.json")
	if err != nil {
		return fmt.Errorf("connectors: temp hosted accounts: %w", err)
	}
	name := tmp.Name()
	defer func() {
		if rerr := os.Remove(name); rerr != nil && !os.IsNotExist(rerr) {
			err = errors.Join(err, fmt.Errorf("connectors: remove temp hosted accounts: %w", rerr))
		}
	}()
	if cerr := tmp.Chmod(0o600); cerr != nil {
		return fmt.Errorf("connectors: chmod hosted accounts: %w", errors.Join(cerr, tmp.Close()))
	}
	if _, werr := tmp.Write(b); werr != nil {
		return fmt.Errorf("connectors: write hosted accounts: %w", errors.Join(werr, tmp.Close()))
	}
	if cerr := tmp.Close(); cerr != nil {
		return fmt.Errorf("connectors: close hosted accounts: %w", cerr)
	}
	if rerr := os.Rename(name, c.path); rerr != nil {
		return fmt.Errorf("connectors: replace hosted accounts: %w", rerr)
	}
	return nil
}
