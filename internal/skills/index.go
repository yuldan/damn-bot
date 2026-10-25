package skills

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// IndexFile is the built skills index below the config dir (SKILL-023).
const IndexFile = "skills-index.json"

// ErrIndex is a malformed or unbounded skills index.
var ErrIndex = errors.New("skills: index error")

// IndexEntry is one indexed skill.
type IndexEntry struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	SHA256      string `json:"sha256"`
	Path        string `json:"path"`
	Source      string `json:"source"`
}

// SkillIndex maps skill name to its indexed entry.
type SkillIndex map[string]IndexEntry

// BuildIndex derives the index from parsed skills (last duplicate wins is not
// possible: the loader already rejects duplicates).
func BuildIndex(list []Skill) SkillIndex {
	idx := make(SkillIndex, len(list))
	for i := range list {
		s := &list[i]
		idx[s.Name] = IndexEntry{Name: s.Name, Description: s.Description, SHA256: s.SHA256, Path: s.Path, Source: string(s.Source)}
	}
	return idx
}

// IndexDelta is the difference between two index snapshots.
type IndexDelta struct {
	Added, Changed, Removed []string
}

// Diff compares two indexes; nil slices mean no change.
func Diff(oldIdx, newIdx SkillIndex) IndexDelta {
	var d IndexDelta
	for name, next := range newIdx {
		prev, ok := oldIdx[name]
		switch {
		case !ok:
			d.Added = append(d.Added, name)
		case prev.SHA256 != next.SHA256:
			d.Changed = append(d.Changed, name)
		}
	}
	for name := range oldIdx {
		if _, ok := newIdx[name]; !ok {
			d.Removed = append(d.Removed, name)
		}
	}
	slices.Sort(d.Added)
	slices.Sort(d.Changed)
	slices.Sort(d.Removed)
	return d
}

// Empty reports whether the delta has no changes.
func (d IndexDelta) Empty() bool { return len(d.Added)+len(d.Changed)+len(d.Removed) == 0 }

// LoadIndex reads <config-dir>/skills-index.json; missing = empty.
func LoadIndex(configDir string) (idx SkillIndex, outErr error) {
	root, err := os.OpenRoot(configDir)
	if errors.Is(err, os.ErrNotExist) {
		return SkillIndex{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrIndex, err)
	}
	defer func() {
		if cerr := root.Close(); cerr != nil && outErr == nil {
			outErr = fmt.Errorf("%w: close config dir: %w", ErrIndex, cerr)
		}
	}()
	f, oerr := root.Open(IndexFile)
	if errors.Is(oerr, os.ErrNotExist) {
		return SkillIndex{}, nil
	}
	if oerr != nil {
		return nil, fmt.Errorf("%w: %w", ErrIndex, oerr)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && outErr == nil {
			outErr = fmt.Errorf("%w: close index: %w", ErrIndex, cerr)
		}
	}()
	var raw map[string]IndexEntry
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if derr := dec.Decode(&raw); derr != nil {
		return nil, fmt.Errorf("%w: %w", ErrIndex, derr)
	}
	return SkillIndex(raw), nil
}

// SaveIndex writes the index atomically (0600, temp + rename) under configDir.
func SaveIndex(configDir string, idx SkillIndex) error {
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return fmt.Errorf("%w: %w", ErrIndex, err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return fmt.Errorf("%w: %w", ErrIndex, err)
	}
	tmp, terr := os.CreateTemp(configDir, ".skills-index-*.json")
	if terr != nil {
		return fmt.Errorf("%w: %w", ErrIndex, terr)
	}
	name := tmp.Name()
	defer func() {
		if rerr := os.Remove(name); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			return
		}
	}()
	if _, werr := tmp.Write(data); werr != nil {
		if cerr := tmp.Close(); cerr != nil {
			return fmt.Errorf("%w: %w", ErrIndex, errors.Join(werr, cerr))
		}
		return fmt.Errorf("%w: %w", ErrIndex, werr)
	}
	if cerr := tmp.Chmod(0o600); cerr != nil {
		if closeErr := tmp.Close(); closeErr != nil {
			return fmt.Errorf("%w: %w", ErrIndex, errors.Join(cerr, closeErr))
		}
		return fmt.Errorf("%w: %w", ErrIndex, cerr)
	}
	if cerr := tmp.Close(); cerr != nil {
		return fmt.Errorf("%w: %w", ErrIndex, cerr)
	}
	if rerr := os.Rename(name, filepath.Join(configDir, IndexFile)); rerr != nil {
		return fmt.Errorf("%w: %w", ErrIndex, rerr)
	}
	return nil
}
