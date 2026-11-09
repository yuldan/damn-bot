package hub

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// Source is one of the four skills-hub sources of SPEC §9.10.
type Source string

// The closed source set. Every source has a fixed host allowlist; a URL is
// accepted only when it is https on one of those hosts, has no userinfo, and
// carries no traversal segment.
const (
	SourceOfficial Source = "official"
	SourceGitHub   Source = "github"
	SourceClawHub  Source = "clawhub"
	SourceSkillsSh Source = "skills.sh"
)

// ErrSource is an unknown source name.
var ErrSource = errors.New("skills hub: unknown source")

// ErrURL is a source URL outside the https host allowlist.
var ErrURL = errors.New("skills hub: source url rejected")

// ErrSignature is a missing or invalid ed25519 index signature.
var ErrSignature = errors.New("skills hub: index signature rejected")

type sourceSpec struct {
	source Source
	base   string
	hosts  []string
}

func defaultSpecs() []sourceSpec {
	return []sourceSpec{
		{SourceOfficial, "https://skills.nousresearch.com", []string{"skills.nousresearch.com"}},
		{SourceGitHub, "https://api.github.com", []string{"api.github.com", "codeload.github.com", "raw.githubusercontent.com"}},
		{SourceClawHub, "https://clawhub.ai", []string{"clawhub.ai"}},
		{SourceSkillsSh, "https://skills.sh", []string{"skills.sh"}},
	}
}

// Sources returns the four sources in registry order.
func Sources() []Source {
	return []Source{SourceOfficial, SourceGitHub, SourceClawHub, SourceSkillsSh}
}

// ParseSource validates a CLI/source-name argument.
func ParseSource(name string) (Source, error) {
	for _, s := range Sources() {
		if string(s) == name {
			return s, nil
		}
	}
	return "", fmt.Errorf("%w: %q", ErrSource, name)
}

func specFor(src Source) (sourceSpec, bool) {
	for _, spec := range defaultSpecs() {
		if spec.source == src {
			return spec, true
		}
	}
	return sourceSpec{}, false
}

// DefaultBaseURL returns the pinned production base URL of a source.
func DefaultBaseURL(src Source) (string, bool) {
	spec, ok := specFor(src)
	return spec.base, ok
}

// ValidateURL enforces the source policy for one fully-qualified URL.
func ValidateURL(src Source, raw string) (*url.URL, error) {
	spec, ok := specFor(src)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrSource, src)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: parse: %w", ErrURL, err)
	}
	if u.Scheme != "https" {
		return nil, fmt.Errorf("%w: scheme %q is not https", ErrURL, u.Scheme)
	}
	if u.User != nil || u.Opaque != "" {
		return nil, fmt.Errorf("%w: userinfo or opaque url", ErrURL)
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" || (u.Port() != "" && u.Port() != "443") {
		return nil, fmt.Errorf("%w: non-default port or empty host", ErrURL)
	}
	if !slices.Contains(spec.hosts, host) {
		return nil, fmt.Errorf("%w: host %q is not allowlisted for %s", ErrURL, host, src)
	}
	if strings.ContainsAny(u.Path, "\\\x00") || strings.Contains(u.EscapedPath(), "..") {
		return nil, fmt.Errorf("%w: traversal in path", ErrURL)
	}
	return u, nil
}

// validateTestURL is the `skillstest`-only seam: a loopback http(s) endpoint
// the e2e harness owns. It never accepts a public host over http, and it is
// unreachable in a release build (only the test composition sets it).
func validateTestURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: parse: %w", ErrURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("%w: scheme %q", ErrURL, u.Scheme)
	}
	if u.User != nil || u.Opaque != "" {
		return nil, fmt.Errorf("%w: userinfo or opaque url", ErrURL)
	}
	host := strings.ToLower(u.Hostname())
	if host != "127.0.0.1" && host != "::1" && host != "localhost" {
		return nil, fmt.Errorf("%w: test endpoint %q is not loopback", ErrURL, host)
	}
	if strings.Contains(u.EscapedPath(), "..") || strings.ContainsAny(u.Path, "\\\x00") {
		return nil, fmt.Errorf("%w: traversal in path", ErrURL)
	}
	return u, nil
}
