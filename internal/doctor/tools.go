//go:build !minimal

package doctor

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ToolsSchema is the embedded lock document (GAP-TOOL-06).
const ToolsSchema = "damnbot.tools/v1"

// MaxToolBinary bounds one installed executable. The largest pinned release
// artifact (Lightpanda) is well below it.
const MaxToolBinary = 256 << 20

// Tool lock errors. None of them carries a URL: a release URL may embed a
// query token, and `verify`/`ls` must never print one.
var (
	ErrToolUnknown    = errors.New("doctor tools: tool is not in the lock")
	ErrToolNoArtifact = errors.New("doctor tools: no pinned artifact")
	ErrToolVerify     = errors.New("doctor tools: installed binary failed verification")
	ErrToolLock       = errors.New("doctor tools: invalid lock")
)

//go:embed tools_manifest.json
var embeddedToolsLock []byte

// ToolArtifact is one pinned (url, sha256, size) row per OS/arch.
type ToolArtifact struct {
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	URL          string `json:"url"`
	SHA256       string `json:"sha256"`
	BinarySHA256 string `json:"binary_sha256"`
	Size         int64  `json:"size"`
	Archive      string `json:"archive,omitempty"` // "" (raw), "tar.gz", "zip"
	Binary       string `json:"binary"`            // member in the archive, or the installed file name
}

// ToolPackage is one optional external executable.
type ToolPackage struct {
	Name       string         `json:"name"`
	Version    string         `json:"version"`
	License    string         `json:"license"`
	LicenseURL string         `json:"license_url"`
	Note       string         `json:"note,omitempty"`
	Artifacts  []ToolArtifact `json:"artifacts"`
}

// ToolsLock is the declarative inventory of optional executables. It is
// embedded in the binary; `doctor tools ls` and `verify` never touch the
// network.
type ToolsLock struct {
	Schema string        `json:"schema"`
	Tools  []ToolPackage `json:"tools"`
}

// EmbeddedToolsLock parses the lock shipped with this binary.
func EmbeddedToolsLock() (*ToolsLock, error) {
	return ParseToolsLock(embeddedToolsLock)
}

// ParseToolsLock validates a lock document. Fail-closed: a lock with an
// unknown schema, a non-https URL, a bad hash, or a traversal path is
// rejected before any fetch.
func ParseToolsLock(data []byte) (*ToolsLock, error) {
	var lock ToolsLock
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&lock); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrToolLock, err)
	}
	if lock.Schema != ToolsSchema {
		return nil, fmt.Errorf("%w: schema %q, want %q", ErrToolLock, lock.Schema, ToolsSchema)
	}
	if len(lock.Tools) == 0 {
		return nil, fmt.Errorf("%w: no tools", ErrToolLock)
	}
	if err := lock.validate(); err != nil {
		return nil, err
	}
	return &lock, nil
}

func (l *ToolsLock) validate() error {
	seen := map[string]bool{}
	for i := range l.Tools {
		if err := validateToolPackage(&l.Tools[i], seen); err != nil {
			return err
		}
	}
	return nil
}

func validateToolPackage(p *ToolPackage, seen map[string]bool) error {
	if !toolNameOK.MatchString(p.Name) || strings.Contains(p.Name, "..") || seen[p.Name] {
		return fmt.Errorf("%w: name %q", ErrToolLock, p.Name)
	}
	seen[p.Name] = true
	if p.License == "" || p.LicenseURL == "" {
		return fmt.Errorf("%w: %s has no license", ErrToolLock, p.Name)
	}
	if _, err := url.ParseRequestURI(p.LicenseURL); err != nil {
		return fmt.Errorf("%w: %s license_url: %w", ErrToolLock, p.Name, err)
	}
	if len(p.Artifacts) > 0 && (!toolVersionOK.MatchString(p.Version) || strings.Contains(p.Version, "..")) {
		return fmt.Errorf("%w: %s version %q", ErrToolLock, p.Name, p.Version)
	}
	platforms := map[string]bool{}
	for j := range p.Artifacts {
		a := &p.Artifacts[j]
		if err := a.validate(); err != nil {
			return fmt.Errorf("%w: %s: %w", ErrToolLock, p.Name, err)
		}
		key := a.OS + "/" + a.Arch
		if platforms[key] {
			return fmt.Errorf("%w: %s has duplicate %s", ErrToolLock, p.Name, key)
		}
		platforms[key] = true
	}
	return nil
}

var (
	toolNameOK    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)
	toolVersionOK = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,31}$`)
	sha256Hex     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func (a *ToolArtifact) validate() error {
	switch a.OS {
	case "linux", "darwin", "windows":
	default:
		return fmt.Errorf("os %q", a.OS)
	}
	switch a.Arch {
	case "amd64", "arm64":
	default:
		return fmt.Errorf("arch %q", a.Arch)
	}
	u, err := url.Parse(a.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return errors.New("url must be an https release URL without credentials")
	}
	if !sha256Hex.MatchString(a.SHA256) || !sha256Hex.MatchString(a.BinarySHA256) {
		return errors.New("sha256 must be lowercase hex")
	}
	if a.Size <= 0 {
		return errors.New("size must be positive")
	}
	switch a.Archive {
	case "", "tar.gz", "zip":
	default:
		return fmt.Errorf("archive %q", a.Archive)
	}
	if !safeMember(a.Binary) {
		return fmt.Errorf("binary %q escapes the install directory", a.Binary)
	}
	return nil
}

// safeMember rejects absolute and parent-relative archive/install names.
func safeMember(name string) bool {
	if name == "" || strings.Contains(name, `\`) || strings.HasPrefix(name, "/") {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(name))
	return clean == name && clean != ".." && !strings.HasPrefix(clean, "../")
}

// Lookup returns the package with name.
func (l *ToolsLock) Lookup(name string) (*ToolPackage, bool) {
	for i := range l.Tools {
		if l.Tools[i].Name == name {
			return &l.Tools[i], true
		}
	}
	return nil, false
}

// Names lists every locked tool in document order.
func (l *ToolsLock) Names() []string {
	out := make([]string, 0, len(l.Tools))
	for _, p := range l.Tools {
		out = append(out, p.Name)
	}
	return out
}

// Artifact returns the pinned row for the requested platform.
func (p *ToolPackage) Artifact(goos, goarch string) (ToolArtifact, bool) {
	for _, a := range p.Artifacts {
		if a.OS == goos && a.Arch == goarch {
			return a, true
		}
	}
	return ToolArtifact{}, false
}

// ToolStatus is one `ls`/`verify` row. Path is inside the data dir; no field
// ever carries a download URL.
type ToolStatus struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	License   string `json:"license"`
	Platform  string `json:"platform,omitempty"`
	Pinned    bool   `json:"pinned"`
	Installed bool   `json:"installed"`
	OK        bool   `json:"ok"`
	Path      string `json:"path,omitempty"`
	Detail    string `json:"detail"`
}

// InstallPath is <data_dir>/tools/<name>/<version>/<binary>.
func InstallPath(root string, p *ToolPackage, a *ToolArtifact) string {
	return filepath.Join(root, "tools", p.Name, p.Version, filepath.Base(a.Binary))
}

// ToolsList reports the lock and installation state without hashing files and
// without network access.
func ToolsList(lock *ToolsLock, root, goos, goarch string) []ToolStatus {
	out := make([]ToolStatus, 0, len(lock.Tools))
	for i := range lock.Tools {
		st := toolRow(&lock.Tools[i], root, goos, goarch)
		out = append(out, st)
	}
	return out
}

func toolRow(p *ToolPackage, root, goos, goarch string) ToolStatus {
	st := ToolStatus{Name: p.Name, Version: p.Version, License: p.License, OK: true}
	a, ok := p.Artifact(goos, goarch)
	if !ok {
		st.Detail = "no pinned artifact for " + goos + "/" + goarch
		if p.Note != "" {
			st.Detail += ": " + p.Note
		}
		return st
	}
	st.Pinned, st.Platform = true, goos+"/"+goarch
	st.Path = InstallPath(root, p, &a)
	if fi, err := os.Stat(st.Path); err == nil && fi.Mode().IsRegular() {
		st.Installed, st.Detail = true, "installed"
	} else {
		st.Detail = "not installed"
	}
	return st
}

// ToolsVerify hashes every installed executable against the lock. A missing
// installation is not a failure; a modified binary is. The returned error
// aggregates failures only, never a URL.
func ToolsVerify(lock *ToolsLock, root, goos, goarch string) ([]ToolStatus, error) {
	out := ToolsList(lock, root, goos, goarch)
	var failed int
	for i := range out {
		st := &out[i]
		if !st.Installed {
			continue
		}
		p, _ := lock.Lookup(st.Name)
		a, _ := p.Artifact(goos, goarch)
		if st.OK = verifyInstalled(st, &a); !st.OK {
			failed++
		}
	}
	if failed > 0 {
		return out, fmt.Errorf("%w: %d installed tool(s) failed", ErrToolVerify, failed)
	}
	return out, nil
}

func verifyInstalled(st *ToolStatus, a *ToolArtifact) bool {
	sum, err := fileSHA256(st.Path)
	switch {
	case err != nil:
		st.Detail = "unreadable: " + err.Error()
		return false
	case sum != a.BinarySHA256:
		st.Detail = "checksum mismatch: installed " + sum
		return false
	}
	st.Detail = "verified"
	return true
}

func fileSHA256(path string) (sum string, err error) {
	f, err := os.DirFS(filepath.Dir(path)).Open(filepath.Base(path))
	if err != nil {
		return "", fmt.Errorf("doctor tools: open: %w", err)
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	h := sha256.New()
	if _, err = io.Copy(h, io.LimitReader(f, MaxToolBinary+1)); err != nil {
		return "", fmt.Errorf("doctor tools: hash: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
