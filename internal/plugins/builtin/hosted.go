package builtin

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/damnbot/damnbot/internal/connectors"
)

// HostedAccount is one account of a hosted connector app (PLUG-035). TokenRef
// is a keychain reference, never the OAuth token value.
type HostedAccount struct {
	Connector string
	ID        string
	Label     string
	TokenRef  string
}

// ErrConnection rejects a malformed hosted connector account.
var ErrConnection = fmt.Errorf("%w: hosted connection", ErrUnknown)

// Connections is the in-process account store the manage_connections surface
// reads. It holds references only; no token value is accepted or returned.
type Connections struct {
	mu       sync.Mutex
	accounts map[string]HostedAccount
}

// NewConnections returns an empty account store.
func NewConnections() *Connections {
	return &Connections{accounts: map[string]HostedAccount{}}
}

func connectorKnown(name string) bool {
	for _, d := range connectors.Builtin() {
		if d.Name == name {
			return true
		}
	}
	return false
}

// Add registers one account. The connector must be in the closed connectors
// registry and tokenRef must be a keychain: reference.
func (c *Connections) Add(connector, id, label, tokenRef string) (HostedAccount, error) {
	if !connectorKnown(connector) {
		return HostedAccount{}, fmt.Errorf("%w: connector %q", ErrConnection, connector)
	}
	if id == "" || label == "" || !strings.HasPrefix(tokenRef, "keychain:") || len(tokenRef) <= len("keychain:") {
		return HostedAccount{}, fmt.Errorf("%w: account %q needs id, label and a keychain reference", ErrConnection, id)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key := connector + "/" + id
	if _, exists := c.accounts[key]; exists {
		return HostedAccount{}, fmt.Errorf("%w: account %q exists", ErrConnection, id)
	}
	acc := HostedAccount{Connector: connector, ID: id, Label: label, TokenRef: tokenRef}
	c.accounts[key] = acc
	return acc, nil
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
	return nil
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
	return nil
}

// List returns the accounts of one connector sorted by id; empty connector
// lists all accounts. Values are references only.
func (c *Connections) List(connector string) []HostedAccount {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]HostedAccount, 0, len(c.accounts))
	for _, acc := range c.accounts {
		if connector == "" || acc.Connector == connector {
			out = append(out, acc)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
