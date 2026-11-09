package hub

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/url"
	"path"

	"github.com/damnbot/damnbot/internal/skills"
)

// Config builds a [Hub]. Fetcher and Service are required: without a fetcher
// the hub cannot talk to a registry, and without a service it cannot put a
// downloaded skill through quarantine.
type Config struct {
	Fetcher Fetcher
	Service *skills.Service
	// Sources overrides a base URL per source. Production leaves it nil; a
	// non-nil value is still held to the https host allowlist.
	Sources map[Source]string
	// AllowTestSources is set only by the `skillstest` e2e build so the
	// harness can point a source at its loopback fake registry. It never
	// accepts a non-loopback host and is false in release builds.
	AllowTestSources bool
}

// Hub is the four-source install path: allowlisted URL -> bounded fetch ->
// sha256 check -> parse -> threat scan -> quarantine. Nothing is executed.
type Hub struct {
	fetch Fetcher
	svc   *skills.Service
	base  map[Source]*url.URL
	// testBase marks the sources whose base URL came from the test-only
	// override; their joined skill URLs use the loopback policy too.
	testBase map[Source]bool
}

// New validates the dependencies and every source base URL.
func New(cfg Config) (*Hub, error) {
	if cfg.Fetcher == nil || cfg.Service == nil {
		return nil, fmt.Errorf("%w: nil fetcher/service", ErrURL)
	}
	h := &Hub{fetch: cfg.Fetcher, svc: cfg.Service, base: map[Source]*url.URL{}, testBase: map[Source]bool{}}
	for _, src := range Sources() {
		raw, override := cfg.Sources[src]
		if !override {
			var ok bool
			if raw, ok = DefaultBaseURL(src); !ok {
				return nil, fmt.Errorf("%w: %q", ErrSource, src)
			}
		}
		u, err := h.checkURL(src, raw, cfg.AllowTestSources && override)
		if err != nil {
			return nil, err
		}
		h.base[src], h.testBase[src] = u, cfg.AllowTestSources && override
	}
	return h, nil
}

// checkURL applies the release policy, or the loopback-only test policy when
// the composition set AllowTestSources.
func (h *Hub) checkURL(src Source, raw string, test bool) (*url.URL, error) {
	if test {
		return validateTestURL(raw)
	}
	return ValidateURL(src, raw)
}

// Index fetches and validates one source's index document.
func (h *Hub) Index(ctx context.Context, src Source) (Index, error) {
	if _, ok := h.base[src]; !ok {
		return Index{}, fmt.Errorf("%w: %q", ErrSource, src)
	}
	u := h.base[src].JoinPath("index.json")
	raw, err := h.fetch.Get(ctx, u, MaxIndexBytes)
	if err != nil {
		return Index{}, fmt.Errorf("skills hub: index %s: %w", src, err)
	}
	return ParseIndex(src, raw)
}

// Download fetches one entry, verifies its mandatory sha256 before parsing,
// then installs it through [skills.Service.Install]. Every source other than
// user/builtin is quarantined, so the result is not retrievable until an
// explicit approval. A hash mismatch writes nothing.
func (h *Hub) Download(ctx context.Context, src Source, e *Entry) (skills.Install, error) {
	if err := e.validate(); err != nil {
		return skills.Install{}, err
	}
	base, ok := h.base[src]
	if !ok {
		return skills.Install{}, fmt.Errorf("%w: %q", ErrSource, src)
	}
	u := base.JoinPath(e.Path)
	if _, uerr := h.checkURL(src, u.String(), h.testBase[src]); uerr != nil {
		return skills.Install{}, uerr
	}
	raw, err := h.fetch.Get(ctx, u, MaxSkillBytes)
	if err != nil {
		return skills.Install{}, fmt.Errorf("skills hub: %s: %w", e.Name, err)
	}
	if herr := verifyHash(raw, e.SHA256); herr != nil {
		return skills.Install{}, fmt.Errorf("%w: %s", herr, e.Name)
	}
	sk, err := skills.Parse(raw, path.Join(string(src), e.Path), skills.SourceHub)
	if err != nil {
		return skills.Install{}, fmt.Errorf("skills hub: %s: %w", e.Name, err)
	}
	if sk.Name != e.Name {
		return skills.Install{}, fmt.Errorf("%w: index name %q, front matter %q", ErrIndex, e.Name, sk.Name)
	}
	inst, err := h.svc.Install(ctx, &sk)
	if err != nil {
		return skills.Install{}, fmt.Errorf("skills hub: install %s: %w", e.Name, err)
	}
	return inst, nil
}

// verifyHash compares the downloaded bytes with the pinned digest in constant
// time. A missing or malformed digest never reaches here: [Entry.validate]
// rejected it, and [ParseIndex] rejects an index entry without one.
func verifyHash(raw []byte, want string) error {
	sum := sha256.Sum256(raw)
	got := make([]byte, hex.EncodedLen(len(sum)))
	hex.Encode(got, sum[:])
	if subtle.ConstantTimeCompare(got, []byte(want)) != 1 {
		return ErrHash
	}
	return nil
}
