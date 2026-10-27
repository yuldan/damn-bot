package skills

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// HostRequirement is the optional host gate of a skill (GAP-TOOL-10,
// SPEC §22): a skill whose requirement is not met is unavailable — excluded
// from the `damnbot skills list` catalog and reported there with the reason.
// The runtime prompt catalog is NOT filtered here; ADR-207 records that
// follow-up as mandatory before PHASE is raised.
//
// Front matter form (strict; unknown keys and unknown tags are errors):
//
//	requires: {bins: [rg@14.1.0, jq], os: [linux, darwin], arch: [amd64], gpu: true}
//
// `bins` entries are `name` or `name@min_version`. The legacy
// `requires: [other-skill]` list (skill dependencies) is still parsed when the
// value is a list, never an object.
type HostRequirement struct {
	Bins []BinRequirement `json:"bins,omitempty"`
	OS   []string         `json:"os,omitempty"`   // accepted runtime.GOOS values; empty = any
	Arch []string         `json:"arch,omitempty"` // accepted runtime.GOARCH values; empty = any
	GPU  bool             `json:"gpu,omitempty"`  // true = a usable GPU is required
}

// BinRequirement is one required executable and its minimum version.
type BinRequirement struct {
	Name       string `json:"name"`
	MinVersion string `json:"min_version,omitempty"` // "" = any version
}

// Host is the injected host facts. A nil detector means "cannot verify", and
// that is reported as an unmet requirement: a requirement must never pass
// silently because nobody checked it.
type Host struct {
	GOOS, GOARCH string
	LookPath     func(string) (string, error)
	Version      func(string) (string, error) // binary name -> version string
	GPU          func() (bool, error)
}

// JSON encodes the requirement for storage in skills.requires_host (the index
// row, so read paths never parse SKILL.md again). A nil requirement stores "".
func (r *HostRequirement) JSON() (string, error) {
	if r == nil {
		return "", nil
	}
	b, err := json.Marshal(r)
	if err != nil {
		return "", fmt.Errorf("skills: encode requires: %w", err)
	}
	return string(b), nil
}

// ParseHostRequirementJSON decodes a stored skills.requires_host value; ""
// means the skill declares no host requirement.
func ParseHostRequirementJSON(s string) (*HostRequirement, error) {
	if s == "" {
		return nil, nil
	}
	var h HostRequirement
	dec := json.NewDecoder(strings.NewReader(s))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&h); err != nil {
		return nil, fmt.Errorf("requires_host: %w", err)
	}
	return &h, nil
}

// Reason is one unmet host requirement.
type Reason struct {
	Field  string // bins | os | arch | gpu
	Detail string
}

// String renders the reason for CLI output.
func (r Reason) String() string { return r.Field + ": " + r.Detail }

// Unmet lists the host requirements this host does not satisfy, in a stable
// order (bins in declaration order, then os, arch, gpu).
func (r *HostRequirement) Unmet(h Host) []Reason {
	var out []Reason
	for _, b := range r.Bins {
		out = append(out, unmetBin(b, h)...)
	}
	if len(r.OS) > 0 && !slices.Contains(r.OS, h.GOOS) {
		out = append(out, Reason{Field: "os", Detail: fmt.Sprintf("%s not in [%s]", h.GOOS, strings.Join(r.OS, " "))})
	}
	if len(r.Arch) > 0 && !slices.Contains(r.Arch, h.GOARCH) {
		out = append(out, Reason{Field: "arch", Detail: fmt.Sprintf("%s not in [%s]", h.GOARCH, strings.Join(r.Arch, " "))})
	}
	if r.GPU {
		out = append(out, unmetGPU(h)...)
	}
	return out
}

// unmetBin checks presence and, when a minimum is declared, the version.
func unmetBin(b BinRequirement, h Host) []Reason {
	if b.Name == "" {
		return []Reason{{Field: "bins", Detail: "empty binary name"}}
	}
	path, err := h.LookPath(b.Name)
	if err != nil || path == "" {
		return []Reason{{Field: "bins", Detail: b.Name + " not found"}}
	}
	if b.MinVersion == "" {
		return nil
	}
	if h.Version == nil {
		return []Reason{{Field: "bins", Detail: b.Name + " version unknown (need >= " + b.MinVersion + ")"}}
	}
	have, verr := h.Version(b.Name)
	if verr != nil || have == "" {
		return []Reason{{Field: "bins", Detail: b.Name + " version unknown (need >= " + b.MinVersion + ")"}}
	}
	less, comparable := versionLess(have, b.MinVersion)
	if !comparable {
		return []Reason{{Field: "bins", Detail: b.Name + " version " + have + " is not comparable to " + b.MinVersion}}
	}
	if less {
		return []Reason{{Field: "bins", Detail: b.Name + " " + have + " < " + b.MinVersion}}
	}
	return nil
}

// unmetGPU fails closed: no detector is an unmet requirement, not a pass.
func unmetGPU(h Host) []Reason {
	if h.GPU == nil {
		return []Reason{{Field: "gpu", Detail: "required but no detector is available"}}
	}
	ok, err := h.GPU()
	switch {
	case err != nil:
		return []Reason{{Field: "gpu", Detail: "required; detector: " + err.Error()}}
	case !ok:
		return []Reason{{Field: "gpu", Detail: "required but none detected"}}
	}
	return nil
}

// Unavailable reports the host requirements of s that h does not meet (nil
// when the skill declares no host requirement).
func (s *Skill) Unavailable(h Host) []Reason {
	if s.Host == nil {
		return nil
	}
	return s.Host.Unmet(h)
}

// versionLess compares two dotted numeric versions. comparable is false when
// either side has a component that is not a number, so the caller reports
// "cannot verify" instead of guessing.
func versionLess(have, want string) (less, comparable bool) {
	a, aok := versionParts(have)
	b, bok := versionParts(want)
	if !aok || !bok {
		return false, false
	}
	for i := range max(len(a), len(b)) {
		var x, y int
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			return x < y, true
		}
	}
	return false, true
}

// versionParts parses "1.2.3" (leading "v" allowed, trailing prerelease
// dropped) into numeric components.
func versionParts(v string) ([]int, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	if v == "" {
		return nil, false
	}
	parts := strings.Split(v, ".")
	out := make([]int, len(parts))
	for i, p := range parts {
		n := 0
		if p == "" || len(p) > 9 {
			return nil, false
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				return nil, false
			}
			n = n*10 + int(r-'0')
		}
		out[i] = n
	}
	return out, true
}

// parseHostRequirement parses the inline flow mapping of `requires: {…}`.
// The schema is closed: an unknown key or tag is an error so a typo can never
// turn into a silently ignored requirement.
func parseHostRequirement(v string) (HostRequirement, error) {
	var h HostRequirement
	body, ok := strings.CutPrefix(strings.TrimSpace(v), "{")
	body, closed := strings.CutSuffix(body, "}")
	if !ok || !closed {
		return HostRequirement{}, fmt.Errorf("%w: requires object must be inline, e.g. {bins: [rg@1.0], os: [linux], gpu: true}", ErrFrontMatter)
	}
	parts, err := flowParts(body)
	if err != nil {
		return HostRequirement{}, err
	}
	if len(parts) == 0 {
		return HostRequirement{}, fmt.Errorf("%w: requires object is empty", ErrFrontMatter)
	}
	seen := map[string]bool{}
	for _, part := range parts {
		key, val, ok := strings.Cut(part, ":")
		if !ok {
			return HostRequirement{}, fmt.Errorf("%w: requires entry %q is not key: value", ErrFrontMatter, part)
		}
		key = strings.ToLower(strings.TrimSpace(key))
		if seen[key] {
			return HostRequirement{}, fmt.Errorf("%w: requires key %q appears twice", ErrFrontMatter, key)
		}
		seen[key] = true
		if err := parseHostKey(&h, key, strings.TrimSpace(val)); err != nil {
			return HostRequirement{}, err
		}
	}
	return h, nil
}

// parseHostKey assigns one key of the requires object.
func parseHostKey(h *HostRequirement, key, val string) error {
	switch key {
	case "bins":
		return parseHostBins(h, val)
	case "os", "arch":
		return parseHostPlatform(h, key, val)
	case "gpu":
		return parseHostGPU(h, val)
	default:
		return fmt.Errorf("%w: unknown requires key %q (bins|os|arch|gpu)", ErrFrontMatter, key)
	}
}

// parseHostBins parses `bins: [name@min_version, name]`.
func parseHostBins(h *HostRequirement, val string) error {
	list, err := flowListAll(val)
	if err != nil {
		return err
	}
	for _, item := range list {
		name, min, _ := strings.Cut(item, "@")
		name = strings.TrimSpace(name)
		if !validBinName(name) {
			return fmt.Errorf("%w: requires.bins %q is not a binary name", ErrFrontMatter, item)
		}
		if min != "" {
			if _, ok := versionParts(min); !ok {
				return fmt.Errorf("%w: requires.bins %q has an invalid version", ErrFrontMatter, item)
			}
		}
		h.Bins = append(h.Bins, BinRequirement{Name: name, MinVersion: min})
	}
	return nil
}

// parseHostPlatform parses `os:`/`arch:` tag lists.
func parseHostPlatform(h *HostRequirement, key, val string) error {
	list, err := flowListAll(val)
	if err != nil {
		return err
	}
	for i := range list {
		if !validTag(list[i]) {
			return fmt.Errorf("%w: requires.%s %q is not a platform tag", ErrFrontMatter, key, list[i])
		}
	}
	if key == "os" {
		h.OS = list
	} else {
		h.Arch = list
	}
	return nil
}

// parseHostGPU parses the boolean `gpu:` requirement.
func parseHostGPU(h *HostRequirement, val string) error {
	switch strings.ToLower(val) {
	case "true":
		h.GPU = true
	case "false":
		h.GPU = false
	default:
		return fmt.Errorf("%w: requires.gpu %q (want true|false)", ErrFrontMatter, val)
	}
	return nil
}

// flowParts splits a flow mapping body on commas outside [] and {}.
func flowParts(body string) ([]string, error) {
	var out []string
	depth := 0
	start := 0
	for i, r := range body {
		switch r {
		case '[', '{':
			depth++
		case ']', '}':
			depth--
			if depth < 0 {
				return nil, fmt.Errorf("%w: unbalanced requires object", ErrFrontMatter)
			}
		case ',':
			if depth == 0 {
				if p := strings.TrimSpace(body[start:i]); p != "" {
					out = append(out, p)
				}
				start = i + 1
			}
		}
	}
	if depth != 0 {
		return nil, fmt.Errorf("%w: unbalanced requires object", ErrFrontMatter)
	}
	if p := strings.TrimSpace(body[start:]); p != "" {
		out = append(out, p)
	}
	return out, nil
}

// flowListAll parses a bracketed list for the requires object.
func flowListAll(v string) ([]string, error) {
	if !strings.HasPrefix(v, "[") || !strings.HasSuffix(v, "]") {
		return nil, fmt.Errorf("%w: requires list must be inline, e.g. [linux, darwin]", ErrFrontMatter)
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(v, "["), "]")
	out := []string{}
	for f := range strings.SplitSeq(inner, ",") {
		if f = unquote(strings.TrimSpace(f)); f != "" {
			out = append(out, f)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: requires list is empty", ErrFrontMatter)
	}
	return out, nil
}

// validBinName accepts one executable name (no path, no spaces, no controls).
func validBinName(name string) bool {
	if name == "" || len(name) > 64 || strings.ContainsAny(name, `/\ `) {
		return false
	}
	for _, r := range name {
		if r < 0x21 || r > 0x7e || r == '"' {
			return false
		}
	}
	return true
}

// validTag accepts one GOOS/GOARCH tag.
func validTag(tag string) bool {
	if tag == "" || len(tag) > 32 {
		return false
	}
	for _, r := range tag {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}
