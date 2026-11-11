// Package setup owns the installer manifest and `damnbot setup --uninstall`.
package setup

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ManifestName is the file the installers write under <state_dir>.
const ManifestName = "install-manifest.json"

// Marker lines written by install.sh and install.ps1.
const (
	BeginMarker = "# >>> damnbot >>>"
	EndMarker   = "# <<< damnbot <<<"
)
const maxManifestSize = 1 << 20

// Manifest is the on-disk record of everything an installer wrote.
type Manifest struct {
	Schema      int      `json:"schema"`
	Version     string   `json:"version"`
	StateDir    string   `json:"state_dir"`
	InstalledAt string   `json:"installed_at"`
	Files       []File   `json:"files"`
	Blocks      []Block  `json:"blocks"`
	Dirs        []string `json:"dirs"`
}

// File is one recorded file and the content hashes around the install.
type File struct {
	Path         string  `json:"path"`
	Kind         string  `json:"kind"`
	SHA256Before *string `json:"sha256_before"`
	SHA256After  string  `json:"sha256_after"`
}

// Block is one marked rc block (the marker lines are included in the file).
type Block struct {
	Path         string `json:"path"`
	Begin        string `json:"begin"`
	End          string `json:"end"`
	AddedNewline bool   `json:"added_newline"`
}

// ReadManifest loads and validates one installer manifest.
func ReadManifest(path string) (*Manifest, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("install manifest: %w", err)
	}
	if info.Size() > maxManifestSize {
		return nil, fmt.Errorf("install manifest: %s exceeds %d bytes", path, maxManifestSize)
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- explicit manifest path supplied by the composition root
	if err != nil {
		return nil, fmt.Errorf("install manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("install manifest: %w", err)
	}
	if err := validate(&m); err != nil {
		return nil, fmt.Errorf("install manifest: %w", err)
	}
	return &m, nil
}

// WriteManifest writes m atomically with mode 0600 (directory 0700).
func WriteManifest(path string, m *Manifest) error {
	if verr := validate(m); verr != nil {
		return verr
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("manifest encode: %w", err)
	}
	return writeManifestFile(path, append(raw, '\n'))
}

func writeManifestFile(path string, raw []byte) (err error) {
	if merr := os.MkdirAll(filepath.Dir(path), 0o700); merr != nil {
		return fmt.Errorf("manifest dir: %w", merr)
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".install-manifest-*")
	if err != nil {
		return fmt.Errorf("manifest temp: %w", err)
	}
	name := f.Name()
	defer func() {
		if cerr := f.Close(); cerr != nil && !errors.Is(cerr, os.ErrClosed) && err == nil {
			err = fmt.Errorf("manifest close: %w", cerr)
		}
		if rerr := os.Remove(name); rerr != nil && !errors.Is(rerr, os.ErrNotExist) && err == nil {
			err = fmt.Errorf("manifest cleanup: %w", rerr)
		}
	}()
	if merr := f.Chmod(0o600); merr != nil {
		return fmt.Errorf("manifest mode: %w", merr)
	}
	if _, werr := f.Write(raw); werr != nil {
		return fmt.Errorf("manifest write: %w", werr)
	}
	if serr := f.Sync(); serr != nil {
		return fmt.Errorf("manifest sync: %w", serr)
	}
	if cerr := f.Close(); cerr != nil {
		return fmt.Errorf("manifest close: %w", cerr)
	}
	if rerr := os.Rename(name, path); rerr != nil {
		return fmt.Errorf("manifest publish: %w", rerr)
	}
	return nil
}

func validate(m *Manifest) error {
	if m.Schema != 1 {
		return fmt.Errorf("schema = %d, want 1", m.Schema)
	}
	if err := safePath(m.StateDir); err != nil {
		return fmt.Errorf("state_dir: %w", err)
	}
	seen, err := validateFiles(m.Files)
	if err != nil {
		return err
	}
	if verr := validateBlocks(m.Blocks, seen); verr != nil {
		return verr
	}
	for i, d := range m.Dirs {
		if err := safePath(d); err != nil {
			return fmt.Errorf("dirs[%d]: %w", i, err)
		}
	}
	return nil
}

func validateFiles(files []File) (map[string]string, error) {
	seen := map[string]string{}
	for i, f := range files {
		if err := safePath(f.Path); err != nil {
			return nil, fmt.Errorf("files[%d]: %w", i, err)
		}
		if _, ok := seen[f.Path]; ok {
			return nil, fmt.Errorf("files[%d]: duplicate path %q", i, f.Path)
		}
		seen[f.Path] = f.Kind
		switch f.Kind {
		case "binary", "completion", "unit", "rc":
		default:
			return nil, fmt.Errorf("files[%d]: unknown kind %q", i, f.Kind)
		}
		if !validSHA(f.SHA256After) {
			return nil, fmt.Errorf("files[%d]: invalid sha256_after", i)
		}
		if f.SHA256Before != nil && !validSHA(*f.SHA256Before) {
			return nil, fmt.Errorf("files[%d]: invalid sha256_before", i)
		}
	}
	return seen, nil
}

func validateBlocks(blocks []Block, seen map[string]string) error {
	paths := map[string]bool{}
	for i, b := range blocks {
		if err := safePath(b.Path); err != nil {
			return fmt.Errorf("blocks[%d]: %w", i, err)
		}
		if b.Begin != BeginMarker || b.End != EndMarker {
			return fmt.Errorf("blocks[%d]: unknown markers", i)
		}
		if seen[b.Path] != "rc" {
			return fmt.Errorf("blocks[%d]: %q is not a recorded rc file", i, b.Path)
		}
		if paths[b.Path] {
			return fmt.Errorf("blocks[%d]: duplicate path %q", i, b.Path)
		}
		paths[b.Path] = true
	}
	return nil
}

func safePath(p string) error {
	if p == "" || strings.ContainsRune(p, 0) {
		return errors.New("empty or NUL path")
	}
	if !filepath.IsAbs(p) {
		return fmt.Errorf("not absolute: %q", p)
	}
	if filepath.Clean(p) != p {
		return fmt.Errorf("not canonical: %q", p)
	}
	if filepath.Dir(p) == p {
		return fmt.Errorf("refuses filesystem root: %q", p)
	}
	return nil
}

func validSHA(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && strings.ToLower(s) == s
}
