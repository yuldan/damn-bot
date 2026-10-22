package skills

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Bundle limits (SKILL-007).
const (
	MaxBundles      = 64
	MaxBundleSkills = 16
	MaxBundleName   = 64
)

// ErrBundle is a rejected bundle definition.
var ErrBundle = errors.New("skills: invalid bundle")

var bundleNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// Bundle is one alias resolving to several skills (SKILL-007).
type Bundle struct {
	Name   string
	Skills []string
}

// ValidateBundle checks the name, member count and that every member exists.
func ValidateBundle(b Bundle, known func(string) bool) error {
	if !bundleNameRe.MatchString(b.Name) {
		return fmt.Errorf("%w: name %q", ErrBundle, b.Name)
	}
	if len(b.Skills) == 0 || len(b.Skills) > MaxBundleSkills {
		return fmt.Errorf("%w: %s needs 1..%d skills", ErrBundle, b.Name, MaxBundleSkills)
	}
	seen := map[string]bool{}
	for _, s := range b.Skills {
		if known != nil && !known(s) {
			return fmt.Errorf("%w: %s references unknown skill %q", ErrBundle, b.Name, s)
		}
		if seen[s] {
			return fmt.Errorf("%w: %s repeats %q", ErrBundle, b.Name, s)
		}
		seen[s] = true
	}
	return nil
}

// Resolve expands one bundle alias to its skill names.
func (b Bundle) Resolve(known func(string) bool) ([]string, error) {
	if err := ValidateBundle(b, known); err != nil {
		return nil, err
	}
	return slices.Clone(b.Skills), nil
}

// LoadBundles reads <config>/bundles.json (missing = no bundles). The format
// is {"alias": ["skill-a","skill-b"]}; unknown JSON fields are rejected.
func LoadBundles(path string) (bundles map[string]Bundle, outErr error) {
	out := map[string]Bundle{}
	root, err := os.OpenRoot(filepath.Dir(path))
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, fmt.Errorf("bundles: %w", err)
	}
	defer func() {
		if cerr := root.Close(); cerr != nil && outErr == nil {
			outErr = fmt.Errorf("bundles: close config dir: %w", cerr)
		}
	}()
	data, rerr := root.ReadFile(filepath.Base(path))
	if errors.Is(rerr, os.ErrNotExist) {
		return out, nil
	}
	if rerr != nil {
		return nil, fmt.Errorf("bundles: %w", rerr)
	}
	var raw map[string][]string
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if derr := dec.Decode(&raw); derr != nil {
		return nil, fmt.Errorf("%w: %w", ErrBundle, derr)
	}
	if len(raw) > MaxBundles {
		return nil, fmt.Errorf("%w: more than %d bundles", ErrBundle, MaxBundles)
	}
	for name, list := range raw {
		out[name] = Bundle{Name: name, Skills: list}
	}
	return out, nil
}

// SaveBundles writes the bundle map atomically (0600, temp file + rename).
func SaveBundles(path string, bundles map[string]Bundle) error {
	raw := make(map[string][]string, len(bundles))
	for name, b := range bundles {
		raw[name] = b.Skills
	}
	data, mErr := json.MarshalIndent(raw, "", "  ")
	if mErr != nil {
		return fmt.Errorf("bundles: %w", mErr)
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("bundles: %w", err)
	}
	tmp, cErr := os.CreateTemp(dir, ".bundles-*.json")
	if cErr != nil {
		return fmt.Errorf("bundles: %w", cErr)
	}
	name := tmp.Name()
	defer func() {
		if rerr := os.Remove(name); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			return
		}
	}()
	if _, werr := tmp.Write(data); werr != nil {
		if cerr := tmp.Close(); cerr != nil {
			return fmt.Errorf("bundles: %w", errors.Join(werr, cerr))
		}
		return fmt.Errorf("bundles: %w", werr)
	}
	if cerr := tmp.Chmod(0o600); cerr != nil {
		if closeErr := tmp.Close(); closeErr != nil {
			return fmt.Errorf("bundles: %w", errors.Join(cerr, closeErr))
		}
		return fmt.Errorf("bundles: %w", cerr)
	}
	if cerr := tmp.Close(); cerr != nil {
		return fmt.Errorf("bundles: %w", cerr)
	}
	if rerr := os.Rename(name, path); rerr != nil {
		return fmt.Errorf("bundles: %w", rerr)
	}
	return nil
}
