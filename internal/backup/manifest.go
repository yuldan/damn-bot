package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
)

// Manifest is the authenticated index of one archive payload.
type Manifest struct {
	Version        int      `json:"v"`
	CreatedAt      int64    `json:"created_at"`
	App            string   `json:"app"`
	IncludeSecrets bool     `json:"include_secrets"`
	Entries        []Entry  `json:"entries"`
	MissingBlobs   []string `json:"missing_blobs,omitempty"`
}

// Entry is one payload file. Path is relative to the payload root.
type Entry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Mode   uint32 `json:"mode"`
	SHA256 string `json:"sha256"`
}

func validPath(p string) bool {
	if p == "" || path.IsAbs(p) || strings.Contains(p, "\\") || path.Clean(p) != p {
		return false
	}
	for part := range strings.SplitSeq(p, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func validateManifest(m *Manifest) error {
	if m.Version != 1 {
		return fmt.Errorf("%w: manifest version", ErrIncompatible)
	}
	if len(m.Entries) == 0 {
		return fmt.Errorf("%w: empty manifest", ErrCorrupt)
	}
	seen := map[string]bool{}
	last := ""
	for i := range m.Entries {
		e := &m.Entries[i]
		if !validPath(e.Path) || e.Size < 0 || e.Mode > 0o7777 {
			return fmt.Errorf("%w: entry %q", ErrCorrupt, e.Path)
		}
		if b, err := hex.DecodeString(e.SHA256); err != nil || len(b) != sha256.Size {
			return fmt.Errorf("%w: entry %q hash", ErrCorrupt, e.Path)
		}
		if seen[e.Path] || e.Path <= last {
			return fmt.Errorf("%w: entry order %q", ErrCorrupt, e.Path)
		}
		seen[e.Path] = true
		last = e.Path
	}
	for _, sha := range m.MissingBlobs {
		if b, err := hex.DecodeString(sha); err != nil || len(b) != sha256.Size {
			return fmt.Errorf("%w: missing blob %q", ErrCorrupt, sha)
		}
	}
	return nil
}

func encodeManifest(m *Manifest) ([]byte, error) {
	sort.Slice(m.Entries, func(i, j int) bool { return m.Entries[i].Path < m.Entries[j].Path })
	sort.Strings(m.MissingBlobs)
	b, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("backup: encode manifest: %w", err)
	}
	return b, nil
}

func decodeManifest(b []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("%w: manifest JSON", ErrCorrupt)
	}
	if err := validateManifest(&m); err != nil {
		return m, err
	}
	return m, nil
}
