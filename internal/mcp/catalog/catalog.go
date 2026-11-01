// Package catalog verifies signed provider/MCP catalog manifests before any
// entry is trusted (GAP-SEC-20, SPEC §22.1). A manifest is useless without a
// valid ed25519 signature from the operator-configured key; a lower serial is
// rejected as a rollback.
package catalog

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// Kind is the catalog a manifest describes.
type Kind string

// Catalog kinds.
const (
	KindProvider Kind = "provider"
	KindMCP      Kind = "mcp"
)

// Errors of manifest parsing and verification. Every one is fail-closed.
var (
	ErrMalformed = errors.New("catalog: malformed manifest")
	ErrKey       = errors.New("catalog: invalid public key")
	ErrUnsigned  = errors.New("catalog: unsigned manifest")
	ErrSignature = errors.New("catalog: signature mismatch")
	ErrRollback  = errors.New("catalog: serial rollback")
	ErrEntry     = errors.New("catalog: invalid entry")
)

// Entry is one catalog row. Both pins are mandatory: the commit the entry
// was authored at and the sha256 of the archive it installs.
type Entry struct {
	ID      string `json:"id"`
	Commit  string `json:"commit"`
	SHA256  string `json:"sha256"`
	Archive string `json:"archive,omitempty"`
}

// Manifest is the signed payload. Signature is the base64 ed25519 signature
// over [Manifest.SigningBytes]; it is never part of those bytes.
type Manifest struct {
	Kind      Kind    `json:"kind"`
	Name      string  `json:"name"`
	Version   string  `json:"version"`
	Serial    int64   `json:"serial"`
	Entries   []Entry `json:"entries"`
	Signature string  `json:"signature,omitempty"`
}

// SigningBytes returns the deterministic bytes a signature covers: the
// manifest with the signature field removed.
func (m *Manifest) SigningBytes() ([]byte, error) {
	copy := m
	copy.Signature = ""
	b, err := json.Marshal(copy)
	if err != nil {
		return nil, fmt.Errorf("catalog: signing bytes: %w", err)
	}
	return b, nil
}

// ParseManifest validates the unsigned shape. It grants no trust.
func ParseManifest(data []byte) (Manifest, error) {
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("%w: %w", ErrMalformed, err)
	}
	if dec.More() {
		return Manifest{}, fmt.Errorf("%w: trailing data", ErrMalformed)
	}
	if err := m.validate(); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

func (m *Manifest) validate() error {
	if m.Kind != KindProvider && m.Kind != KindMCP {
		return fmt.Errorf("%w: kind %q", ErrMalformed, m.Kind)
	}
	if m.Name == "" || m.Version == "" || m.Serial < 1 {
		return fmt.Errorf("%w: name, version and serial are required", ErrMalformed)
	}
	if len(m.Entries) == 0 || len(m.Entries) > 4096 {
		return fmt.Errorf("%w: entries", ErrMalformed)
	}
	for i := range m.Entries {
		e := &m.Entries[i]
		if e.ID == "" || len(e.ID) > 128 {
			return fmt.Errorf("%w: entry %d id", ErrEntry, i)
		}
		if len(e.Commit) != 40 {
			return fmt.Errorf("%w: %s commit", ErrEntry, e.ID)
		}
		if _, err := hex.DecodeString(e.Commit); err != nil {
			return fmt.Errorf("%w: %s commit", ErrEntry, e.ID)
		}
		if len(e.SHA256) != 64 {
			return fmt.Errorf("%w: %s sha256", ErrEntry, e.ID)
		}
		if _, err := hex.DecodeString(e.SHA256); err != nil {
			return fmt.Errorf("%w: %s sha256", ErrEntry, e.ID)
		}
	}
	return nil
}

// Verify parses data, requires a signature, verifies it with pub, and rejects
// a serial at or below lastSerial. It returns the manifest only after every
// check passed.
func Verify(data []byte, pub ed25519.PublicKey, lastSerial int64) (Manifest, error) {
	m, err := ParseManifest(data)
	if err != nil {
		return Manifest{}, err
	}
	if m.Signature == "" {
		return Manifest{}, ErrUnsigned
	}
	if len(pub) != ed25519.PublicKeySize {
		return Manifest{}, ErrKey
	}
	sig, derr := base64.StdEncoding.Strict().DecodeString(m.Signature)
	if derr != nil || len(sig) != ed25519.SignatureSize {
		return Manifest{}, ErrSignature
	}
	signing, serr := m.SigningBytes()
	if serr != nil {
		return Manifest{}, serr
	}
	if !ed25519.Verify(pub, signing, sig) {
		return Manifest{}, ErrSignature
	}
	if m.Serial <= lastSerial {
		return Manifest{}, fmt.Errorf("%w: serial %d <= %d", ErrRollback, m.Serial, lastSerial)
	}
	return m, nil
}

// SeqStore is the last verified serial of one (kind, name) catalog.
type SeqStore interface {
	Last(kind Kind, name string) (int64, error)
	Save(kind Kind, name string, serial int64) error
}

// VerifyWithStore verifies data and advances the stored serial only after a
// successful signature check. A store failure is returned, never ignored.
func VerifyWithStore(data []byte, pub ed25519.PublicKey, store SeqStore) (Manifest, error) {
	if store == nil {
		return Manifest{}, fmt.Errorf("%w: no serial store", ErrMalformed)
	}
	head, err := ParseManifest(data)
	if err != nil {
		return Manifest{}, err
	}
	last, err := store.Last(head.Kind, head.Name)
	if err != nil {
		return Manifest{}, fmt.Errorf("catalog: read serial: %w", err)
	}
	m, err := Verify(data, pub, last)
	if err != nil {
		return Manifest{}, err
	}
	if err := store.Save(m.Kind, m.Name, m.Serial); err != nil {
		return Manifest{}, fmt.Errorf("catalog: save serial: %w", err)
	}
	return m, nil
}
