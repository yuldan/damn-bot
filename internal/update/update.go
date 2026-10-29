package update

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"

	"github.com/damnbot/damnbot/internal/netx"
)

const (
	schemaVersion    = 1
	manifestAsset    = "release.json"
	manifestSigAsset = "release.json.sig"
	defaultFeedURL   = "https://api.github.com/repos/yuldan/damn-bot/releases"
	channelStable    = "stable"
	channelBeta      = "beta"
	maxFeedSize      = 4 << 20
	maxManifestSize  = 1 << 20
	maxSigSize       = 4 << 10
)

var errNoPublicKey = errors.New("update: release public key is not configured")

type asset struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

type manifest struct {
	Schema      int     `json:"schema"`
	Channel     string  `json:"channel"`
	Version     string  `json:"version"`
	Commit      string  `json:"commit"`
	PublishedAt string  `json:"published_at"`
	Changelog   string  `json:"changelog"`
	Assets      []asset `json:"assets"`
}

type resolved struct {
	rel   ghRelease
	man   manifest
	asset asset
}

// Check resolves and verifies the newest release without installing anything.
func Check(ctx context.Context, client *netx.Client, feed, channel, current string, pub ed25519.PublicKey) (target, changelog string, available bool, err error) {
	r, err := resolve(ctx, client, feed, channel, pub)
	if err != nil {
		return "", "", false, err
	}
	available, err = newerThan(r.man.Version, current)
	if err != nil {
		return "", "", false, err
	}
	changelog = r.man.Changelog
	if available {
		// The signed manifest carries the version entries; show only the
		// slice between the installed and target versions (GAP-INF-04).
		changelog = sliceChangelog(changelog, current, r.man.Version)
	}
	return r.man.Version, changelog, available, nil
}

func resolve(ctx context.Context, client *netx.Client, feed, channel string, pub ed25519.PublicKey) (resolved, error) {
	if client == nil {
		return resolved{}, errors.New("update: nil HTTP client")
	}
	if pub == nil {
		return resolved{}, errNoPublicKey
	}
	if feed == "" {
		feed = defaultFeedURL
	}
	arch, err := nativeArch()
	if err != nil {
		return resolved{}, fmt.Errorf("update: native architecture: %w", err)
	}
	rel, err := latestRelease(ctx, client, feed, channel)
	if err != nil {
		return resolved{}, err
	}
	man, err := verifiedManifest(ctx, client, rel, channel, pub)
	if err != nil {
		return resolved{}, err
	}
	a, ok := man.assetFor(runtime.GOOS, arch)
	if !ok {
		return resolved{}, fmt.Errorf("update: release %s has no asset for %s/%s", man.Version, runtime.GOOS, arch)
	}
	return resolved{rel: rel, man: man, asset: a}, nil
}

func verifiedManifest(ctx context.Context, client *netx.Client, rel ghRelease, channel string, pub ed25519.PublicKey) (manifest, error) {
	manBytes, err := fetchAsset(ctx, client, rel, manifestAsset, maxManifestSize)
	if err != nil {
		return manifest{}, err
	}
	sigBytes, err := fetchAsset(ctx, client, rel, manifestSigAsset, maxSigSize)
	if err != nil {
		return manifest{}, err
	}
	sig, err := parseSignature(sigBytes)
	if err != nil {
		return manifest{}, err
	}
	if !ed25519.Verify(pub, manBytes, sig) {
		return manifest{}, errors.New("update: release manifest signature is invalid")
	}
	man, err := parseManifest(manBytes)
	if err != nil {
		return manifest{}, err
	}
	if man.Channel != channel {
		return manifest{}, fmt.Errorf("update: manifest channel %q does not match requested %q", man.Channel, channel)
	}
	if got := strings.TrimPrefix(rel.TagName, "v"); got != man.Version {
		return manifest{}, fmt.Errorf("update: release tag %q does not match manifest version %q", rel.TagName, man.Version)
	}
	return man, nil
}

func parseManifest(data []byte) (manifest, error) {
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("update: release manifest: %w", err)
	}
	if err := m.validate(); err != nil {
		return m, err
	}
	return m, nil
}

func (m *manifest) assetFor(goos, arch string) (asset, bool) {
	for _, a := range m.Assets {
		if a.OS == goos && a.Arch == arch {
			return a, true
		}
	}
	return asset{}, false
}

func (m *manifest) validate() error {
	if m.Schema != schemaVersion || (m.Channel != channelStable && m.Channel != channelBeta) {
		return fmt.Errorf("update: manifest schema/channel is not supported")
	}
	if _, ok := parseSemver(m.Version); !ok {
		return fmt.Errorf("update: manifest version %q is not semver", m.Version)
	}
	if strings.TrimSpace(m.Changelog) == "" || len(m.Assets) == 0 {
		return errors.New("update: manifest is missing changelog/assets")
	}
	return m.validateAssets()
}

func (m *manifest) validateAssets() error {
	seen := map[string]bool{}
	for _, a := range m.Assets {
		if !validOSArch(a.OS, a.Arch) || len(a.SHA256) != 64 || strings.Trim(a.SHA256, "0123456789abcdef") != "" {
			return fmt.Errorf("update: manifest asset %q has an unsupported platform or sha256", a.Name)
		}
		if a.Name != assetName(m.Version, a.OS, a.Arch) {
			return fmt.Errorf("update: manifest asset name %q does not match %s", a.Name, m.Version)
		}
		key := a.OS + "/" + a.Arch
		if seen[key] {
			return fmt.Errorf("update: manifest lists %s twice", key)
		}
		seen[key] = true
	}
	return nil
}

func validOSArch(goos, arch string) bool {
	return (goos == "linux" || goos == "darwin" || goos == "windows") && (arch == "amd64" || arch == "arm64")
}

func assetName(version, goos, arch string) string {
	if goos == "windows" {
		return "damnbot_" + version + "_" + goos + "_" + arch + ".exe"
	}
	return "damnbot_" + version + "_" + goos + "_" + arch
}

type ghRelease struct {
	TagName    string    `json:"tag_name"`
	Draft      bool      `json:"draft"`
	Prerelease bool      `json:"prerelease"`
	Assets     []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func latestRelease(ctx context.Context, c *netx.Client, feed, channel string) (ghRelease, error) {
	data, err := fetch(ctx, c, feed, maxFeedSize)
	if err != nil {
		return ghRelease{}, err
	}
	var releases []ghRelease
	if err := json.Unmarshal(data, &releases); err != nil {
		return ghRelease{}, fmt.Errorf("update: decode release feed: %w", err)
	}
	for _, r := range releases {
		if r.Draft || r.TagName == "" || (channel == channelStable && r.Prerelease) {
			continue
		}
		return r, nil
	}
	return ghRelease{}, fmt.Errorf("update: no %s release found in the feed", channel)
}

func assetURL(rel ghRelease, name string) (string, error) {
	for _, a := range rel.Assets {
		if a.Name == name && a.BrowserDownloadURL != "" {
			return a.BrowserDownloadURL, nil
		}
	}
	return "", fmt.Errorf("update: release %s has no %s asset", rel.TagName, name)
}

func fetchAsset(ctx context.Context, c *netx.Client, rel ghRelease, name string, max int64) ([]byte, error) {
	for _, a := range rel.Assets {
		if a.Name == name && a.BrowserDownloadURL != "" {
			return fetch(ctx, c, a.BrowserDownloadURL, max)
		}
	}
	return nil, fmt.Errorf("update: release %s has no %s asset", rel.TagName, name)
}

func loopbackHTTP(u *url.URL) bool {
	return u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost")
}

func validateDownloadURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Host == "" || (u.Scheme != "https" && !loopbackHTTP(u)) {
		return errors.New("update: release asset URL must be http(s) without userinfo")
	}
	return nil
}

func fetch(ctx context.Context, c *netx.Client, rawURL string, max int64) (data []byte, err error) {
	if verr := validateDownloadURL(rawURL); verr != nil {
		return nil, verr
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("update: build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "damnbot-update")
	resp, err := c.DoOK(req)
	if err != nil {
		return nil, fmt.Errorf("update: fetch: %w", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("update: close response: %w", cerr)
		}
	}()
	data, err = io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, fmt.Errorf("update: read response: %w", err)
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("update: response exceeds %d bytes", max)
	}
	return data, nil
}
