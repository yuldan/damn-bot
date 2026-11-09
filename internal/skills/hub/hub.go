package hub

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/url"
	"path"
	"slices"
	"strings"

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
	// Owner is the operator identity allowed to approve a quarantined skill.
	// Empty means approval is closed: an owner-less process can download and
	// review, but only an explicit owner can promote a skill.
	Owner string
	// IndexKey is the ed25519 public key that must sign index.json (detached
	// index.json.sig, hex). A nil/short key fails closed: the hub never reads
	// an unsigned index. There is no fallback to the embedded catalog.
	IndexKey ed25519.PublicKey
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
	owner    string
	indexKey ed25519.PublicKey
}

// New validates the dependencies and every source base URL.
func New(cfg *Config) (*Hub, error) {
	if cfg == nil || cfg.Fetcher == nil || cfg.Service == nil {
		return nil, fmt.Errorf("%w: nil fetcher/service", ErrURL)
	}
	h := &Hub{fetch: cfg.Fetcher, svc: cfg.Service, base: map[Source]*url.URL{}, testBase: map[Source]bool{}, owner: cfg.Owner, indexKey: slices.Clone(cfg.IndexKey)}
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
	if verr := h.verifyIndex(ctx, src, raw); verr != nil {
		return Index{}, verr
	}
	return ParseIndex(src, raw)
}

// verifyIndex enforces the detached ed25519 signature of index.json. The
// signature is hex over the exact index bytes; a missing key, missing/extra
// signature file, or any signature failure rejects the index. There is no
// unsigned fallback.
func (h *Hub) verifyIndex(ctx context.Context, src Source, raw []byte) error {
	if len(h.indexKey) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: no trusted index key", ErrSignature)
	}
	sigRaw, err := h.fetch.Get(ctx, h.base[src].JoinPath("index.json.sig"), 256)
	if err != nil {
		return fmt.Errorf("skills hub: signature %s: %w", src, err)
	}
	sig, derr := hex.DecodeString(strings.TrimSpace(string(sigRaw)))
	if derr != nil || len(sig) != ed25519.SignatureSize || !ed25519.Verify(h.indexKey, raw, sig) {
		return fmt.Errorf("%w: %s", ErrSignature, src)
	}
	return nil
}

// Search returns entries whose name or description contains query (case
// insensitive), sorted by name and capped to a positive limit ≤ 100. An empty
// query lists the whole index.
func (h *Hub) Search(ctx context.Context, src Source, query string, limit int) ([]Entry, error) {
	idx, err := h.Index(ctx, src)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	q := strings.ToLower(strings.TrimSpace(query))
	out := make([]Entry, 0, len(idx.Entries))
	for i := range idx.Entries {
		e := &idx.Entries[i]
		if q == "" || strings.Contains(strings.ToLower(e.Name), q) || strings.Contains(strings.ToLower(e.Description), q) {
			out = append(out, *e)
		}
	}
	slices.SortFunc(out, func(a, b Entry) int { return strings.Compare(a.Name, b.Name) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Download fetches one entry, verifies its mandatory sha256 before parsing,
// then installs it through [skills.Service.Install]. Every source other than
// user/builtin is quarantined, so the result is not retrievable until an
// explicit approval. A hash mismatch writes nothing.
func (h *Hub) Download(ctx context.Context, src Source, e *Entry) (skills.Install, error) {
	sk, err := h.parseEntry(ctx, src, e)
	if err != nil {
		return skills.Install{}, err
	}
	inst, ierr := h.svc.Install(ctx, sk)
	if ierr != nil {
		return skills.Install{}, fmt.Errorf("skills hub: install %s: %w", e.Name, ierr)
	}
	return inst, nil
}

// parseEntry fetches, hash-checks, and parses one entry without touching the
// store. Both Download and the atomic install path start here, so the bytes
// written to disk are the same bytes whose sha256 and front matter passed.
func (h *Hub) parseEntry(ctx context.Context, src Source, e *Entry) (*skills.Skill, error) {
	raw, err := h.fetchEntry(ctx, src, e)
	if err != nil {
		return nil, err
	}
	return parseRaw(src, e, raw)
}

// parseRaw validates the front matter of verified bytes and pins the index
// name to the front-matter name.
func parseRaw(src Source, e *Entry, raw []byte) (*skills.Skill, error) {
	sk, err := skills.Parse(raw, path.Join(string(src), e.Path), skills.SourceHub)
	if err != nil {
		return nil, fmt.Errorf("skills hub: %s: %w", e.Name, err)
	}
	if sk.Name != e.Name {
		return nil, fmt.Errorf("%w: index name %q, front matter %q", ErrIndex, e.Name, sk.Name)
	}
	return &sk, nil
}

// fetchEntry is the allowlisted bounded fetch plus the mandatory digest check.
func (h *Hub) fetchEntry(ctx context.Context, src Source, e *Entry) ([]byte, error) {
	if err := e.validate(); err != nil {
		return nil, err
	}
	base, ok := h.base[src]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrSource, src)
	}
	u := base.JoinPath(e.Path)
	if _, uerr := h.checkURL(src, u.String(), h.testBase[src]); uerr != nil {
		return nil, uerr
	}
	raw, err := h.fetch.Get(ctx, u, MaxSkillBytes)
	if err != nil {
		return nil, fmt.Errorf("skills hub: %s: %w", e.Name, err)
	}
	if herr := verifyHash(raw, e.SHA256); herr != nil {
		return nil, fmt.Errorf("%w: %s", herr, e.Name)
	}
	return raw, nil
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
