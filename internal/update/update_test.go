package update

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/netx"
)

func testKey(t testing.TB) (ed25519.PrivateKey, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return priv, pub
}

func testAssets(version string) []asset {
	return []asset{
		{OS: "linux", Arch: "amd64", Name: assetName(version, "linux", "amd64"), SHA256: strings.Repeat("a", 64)},
		{OS: "linux", Arch: "arm64", Name: assetName(version, "linux", "arm64"), SHA256: strings.Repeat("b", 64)},
		{OS: "darwin", Arch: "amd64", Name: assetName(version, "darwin", "amd64"), SHA256: strings.Repeat("c", 64)},
		{OS: "darwin", Arch: "arm64", Name: assetName(version, "darwin", "arm64"), SHA256: strings.Repeat("d", 64)},
		{OS: "windows", Arch: "amd64", Name: assetName(version, "windows", "amd64"), SHA256: strings.Repeat("e", 64)},
		{OS: "windows", Arch: "arm64", Name: assetName(version, "windows", "arm64"), SHA256: strings.Repeat("f", 64)},
	}
}

func testManifest(version, channel string) manifest {
	return manifest{
		Schema:      schemaVersion,
		Channel:     channel,
		Version:     version,
		Commit:      strings.Repeat("1", 40),
		PublishedAt: "2026-10-27T00:00:00Z",
		Changelog:   "- rilis uji",
		Assets:      testAssets(version),
	}
}

func signed(t testing.TB, key ed25519.PrivateKey, man *manifest) ([]byte, []byte) {
	t.Helper()
	data, err := json.Marshal(man)
	if err != nil {
		t.Fatal(err)
	}
	return data, formatSignature(ed25519.Sign(key, data))
}

type feedFixture struct {
	releases []ghRelease
	files    map[string][]byte
}

// serveFeed publishes a GitHub-shaped release list plus the files its assets
// point at. The returned server is local-only (netx test policy).
func serveFeed(t testing.TB, fixture feedFixture) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	releases := fixture.releases
	for i := range releases {
		for j := range releases[i].Assets {
			a := &releases[i].Assets[j]
			if a.BrowserDownloadURL == "" {
				a.BrowserDownloadURL = srv.URL + "/" + a.Name
			}
		}
	}
	mux.HandleFunc("/releases", func(w http.ResponseWriter, _ *http.Request) {
		if err := json.NewEncoder(w).Encode(releases); err != nil {
			t.Errorf("encode releases: %v", err)
		}
	})
	for name, data := range fixture.files {
		mux.HandleFunc("/"+name, func(w http.ResponseWriter, _ *http.Request) {
			if _, err := w.Write(data); err != nil {
				t.Errorf("write %s: %v", name, err)
			}
		})
	}
	return srv
}

func check(t *testing.T, srv *httptest.Server, pub ed25519.PublicKey, channel string) (string, string, bool, error) {
	t.Helper()
	return Check(t.Context(), netx.New(netx.Config{}).Client("update", netx.Background),
		srv.URL+"/releases", channel, "1.0.0", pub)
}

func TestCheckAvailable(t *testing.T) {
	priv, pub := testKey(t)
	man := testManifest("2.0.0", channelStable)
	body, sig := signed(t, priv, &man)
	srv := serveFeed(t, feedFixture{
		releases: []ghRelease{{TagName: "v2.0.0", Assets: []ghAsset{{Name: manifestAsset}, {Name: manifestSigAsset}}}},
		files:    map[string][]byte{manifestAsset: body, manifestSigAsset: sig},
	})
	version, _, available, err := check(t, srv, pub, channelStable)
	if err != nil || !available || version != "2.0.0" {
		t.Fatalf("Check = %q/%t/%v", version, available, err)
	}
}

func TestCheckUpToDate(t *testing.T) {
	priv, pub := testKey(t)
	man := testManifest("1.0.0", channelStable)
	body, sig := signed(t, priv, &man)
	srv := serveFeed(t, feedFixture{
		releases: []ghRelease{{TagName: "v1.0.0", Assets: []ghAsset{{Name: manifestAsset}, {Name: manifestSigAsset}}}},
		files:    map[string][]byte{manifestAsset: body, manifestSigAsset: sig},
	})
	if _, _, available, err := check(t, srv, pub, channelStable); err != nil || available {
		t.Fatalf("up-to-date release: available=%t err=%v", available, err)
	}
}

func TestCheckRejectsBadSignature(t *testing.T) {
	priv, pub := testKey(t)
	man := testManifest("2.0.0", channelStable)
	body, _ := signed(t, priv, &man)
	_, otherSig := signed(t, priv, &manifest{Schema: 1})
	srv := serveFeed(t, feedFixture{
		releases: []ghRelease{{TagName: "v2.0.0", Assets: []ghAsset{{Name: manifestAsset}, {Name: manifestSigAsset}}}},
		files:    map[string][]byte{manifestAsset: body, manifestSigAsset: otherSig},
	})
	if _, _, _, err := check(t, srv, pub, channelStable); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("bad signature: err = %v", err)
	}
}

func TestCheckRejectsTamperedManifest(t *testing.T) {
	priv, pub := testKey(t)
	man := testManifest("2.0.0", channelStable)
	body, sig := signed(t, priv, &man)
	body = bytes.Replace(body, []byte(`"- rilis uji"`), []byte(`"- diubah"`), 1)
	srv := serveFeed(t, feedFixture{
		releases: []ghRelease{{TagName: "v2.0.0", Assets: []ghAsset{{Name: manifestAsset}, {Name: manifestSigAsset}}}},
		files:    map[string][]byte{manifestAsset: body, manifestSigAsset: sig},
	})
	if _, _, _, err := check(t, srv, pub, channelStable); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("tampered manifest: err = %v", err)
	}
}

func checkError(t *testing.T, tag string, man *manifest, channel, want string) {
	t.Helper()
	priv, pub := testKey(t)
	body, sig := signed(t, priv, man)
	srv := serveFeed(t, feedFixture{
		releases: []ghRelease{{TagName: tag, Assets: []ghAsset{{Name: manifestAsset}, {Name: manifestSigAsset}}}},
		files:    map[string][]byte{manifestAsset: body, manifestSigAsset: sig},
	})
	if _, _, _, err := check(t, srv, pub, channel); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("%s: err = %v, want %q", tag, err, want)
	}
}

func TestCheckRejectsOlderTarget(t *testing.T) {
	m0 := testManifest("0.9.0", channelStable)
	checkError(t, "v0.9.0", &m0, channelStable, "older")
}

func TestCheckRejectsChannelMismatch(t *testing.T) {
	m1 := testManifest("2.0.0", channelBeta)
	checkError(t, "v2.0.0", &m1, channelStable, "channel")
}

func TestCheckRejectsTagMismatch(t *testing.T) {
	m2 := testManifest("2.0.0", channelStable)
	checkError(t, "v9.9.9", &m2, channelStable, "tag")
}

func TestCheckChannels(t *testing.T) {
	priv, pub := testKey(t)
	stable := testManifest("2.0.0", channelStable)
	stableBody, stableSig := signed(t, priv, &stable)
	beta := testManifest("2.1.0-rc.1", channelBeta)
	betaBody, betaSig := signed(t, priv, &beta)
	srv := serveFeed(t, feedFixture{
		releases: []ghRelease{
			{TagName: "v2.1.0-rc.1", Prerelease: true, Assets: []ghAsset{{Name: manifestAsset}, {Name: manifestSigAsset}}},
			{TagName: "v2.0.0", Assets: []ghAsset{{Name: manifestAsset}, {Name: manifestSigAsset}}},
		},
		files: map[string][]byte{
			manifestAsset: stableBody, manifestSigAsset: stableSig,
			"beta-" + manifestAsset: betaBody, "beta-" + manifestSigAsset: betaSig,
		},
	})
	// Both releases point at the same asset names in this fixture; the stable
	// selection must be the non-prerelease release regardless of order.
	version, _, _, err := check(t, srv, pub, channelStable)
	if err != nil || version != "2.0.0" {
		t.Fatalf("stable selection = %s, %v", version, err)
	}
}

func TestCheckBetaSelectsPrerelease(t *testing.T) {
	priv, pub := testKey(t)
	man := testManifest("2.1.0-rc.1", channelBeta)
	body, sig := signed(t, priv, &man)
	srv := serveFeed(t, feedFixture{
		releases: []ghRelease{{TagName: "v2.1.0-rc.1", Prerelease: true, Assets: []ghAsset{{Name: manifestAsset}, {Name: manifestSigAsset}}}},
		files:    map[string][]byte{manifestAsset: body, manifestSigAsset: sig},
	})
	version, _, available, err := check(t, srv, pub, channelBeta)
	if err != nil || version != "2.1.0-rc.1" || !available {
		t.Fatalf("beta selection = %s/%t, %v", version, available, err)
	}
}

func TestNativeArchSelect(t *testing.T) {
	man := testManifest("2.0.0", channelStable)
	got, ok := man.assetFor("windows", "arm64")
	if !ok || got.Name != assetName("2.0.0", "windows", "arm64") {
		t.Fatalf("windows/arm64 asset = %+v, %t", got, ok)
	}
	if _, ok := man.assetFor("windows", "amd64"); !ok {
		t.Fatal("fixture lacks the amd64 asset")
	}
}

func TestParseManifestRejectsInvalid(t *testing.T) {
	valid := testManifest("2.0.0", channelStable)
	mutate := func(f func(*manifest)) []byte {
		m := valid
		m.Assets = append([]asset(nil), valid.Assets...)
		f(&m)
		data, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	cases := map[string][]byte{
		"schema":        mutate(func(m *manifest) { m.Schema = 2 }),
		"channel":       mutate(func(m *manifest) { m.Channel = "staging" }),
		"version":       mutate(func(m *manifest) { m.Version = "2.0" }),
		"changelog":     mutate(func(m *manifest) { m.Changelog = " " }),
		"no assets":     mutate(func(m *manifest) { m.Assets = nil }),
		"bad platform":  mutate(func(m *manifest) { m.Assets[0].OS = "plan9" }),
		"bad name":      mutate(func(m *manifest) { m.Assets[0].Name = "damnbot.exe" }),
		"bad sha":       mutate(func(m *manifest) { m.Assets[0].SHA256 = "xyz" }),
		"duplicate":     mutate(func(m *manifest) { m.Assets[1] = m.Assets[0] }),
		"unknown field": []byte(`{"schema":1,"channel":"stable","version":"2.0.0","commit":"1111111111111111111111111111111111111111","published_at":"2026-10-27T00:00:00Z","changelog":"x","assets":[],"extra":true}`),
		"trailing":      append(mutate(func(*manifest) {}), []byte(` {}`)...),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseManifest(data); err == nil {
				t.Fatalf("parseManifest accepted %s", data)
			}
		})
	}
}

func TestParseManifestValid(t *testing.T) {
	man := testManifest("2.0.0", channelStable)
	data, err := json.Marshal(man)
	if err != nil {
		t.Fatal(err)
	}
	got, err := parseManifest(data)
	if err != nil {
		t.Fatalf("parseManifest: %v", err)
	}
	if got.Version != man.Version || len(got.Assets) != len(man.Assets) {
		t.Fatalf("got %+v", got)
	}
}

func TestConfigValidate(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		ok   bool
	}{
		{"default", DefaultConfig(), true},
		{"beta https", Config{Channel: channelBeta, FeedURL: "https://example.test/releases"}, true},
		{"loopback http", Config{Channel: channelStable, FeedURL: "http://127.0.0.1:8080/releases"}, true},
		{"public http", Config{Channel: channelStable, FeedURL: "http://example.test/releases"}, false},
		{"userinfo", Config{Channel: channelStable, FeedURL: "https://user@example.test/releases"}, false},
		{"relative", Config{Channel: channelStable, FeedURL: "/releases"}, false},
		{"channel", Config{Channel: "staging"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.cfg.Validate(); (err == nil) != tc.ok {
				t.Fatalf("Validate() = %v, want ok=%t", err, tc.ok)
			}
		})
	}
}

func TestParsePublicKey(t *testing.T) {
	_, pub := testKey(t)
	good := base64.StdEncoding.EncodeToString(pub)
	if got, err := ParsePublicKey(good); err != nil || !bytes.Equal(got, pub) {
		t.Fatalf("ParsePublicKey: %v", err)
	}
	for name, text := range map[string]string{
		"empty":  "",
		"base64": "!!!",
		"short":  base64.StdEncoding.EncodeToString([]byte("short")),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParsePublicKey(text); err == nil {
				t.Fatalf("ParsePublicKey accepted %q", text)
			}
		})
	}
	if _, err := ParsePublicKey(""); !errors.Is(err, errNoPublicKey) {
		t.Fatalf("empty key = %v, want errNoPublicKey", err)
	}
}

func TestSemverCompare(t *testing.T) {
	cases := []struct {
		target, current string
		newer           bool
		bad             bool
	}{
		{"1.0.0", "1.0.0", false, false},
		{"1.0.1", "1.0.0", true, false},
		{"1.0.0", "1.0.1", false, true},
		{"2.0.0-rc.1", "2.0.0", false, true},
		{"2.0.0", "2.0.0-rc.1", true, false},
		{"2.0.0-rc.10", "2.0.0-rc.2", true, false},
		{"2.0.0-alpha.1", "2.0.0-alpha", true, false},
		{"2.0.0", "dev", true, false},
		{"2.0", "1.0.0", false, true},
	}
	for _, tc := range cases {
		got, err := newerThan(tc.target, tc.current)
		if (err != nil) != tc.bad {
			t.Errorf("newerThan(%s,%s) error = %v, want bad=%t", tc.target, tc.current, err, tc.bad)
			continue
		}
		if err == nil && got != tc.newer {
			t.Errorf("newerThan(%s,%s) = %t, want %t", tc.target, tc.current, got, tc.newer)
		}
	}
}

// TestVerifyReleaseManifestFile always verifies an in-memory manifest with the
// product code; when the release workflow sets DAMNBOT_RELEASE_MANIFEST,
// DAMNBOT_RELEASE_MANIFEST_SIG and DAMNBOT_RELEASE_PUBKEY it verifies the
// actual file before upload (ADR-216).
func TestVerifyReleaseManifestFile(t *testing.T) {
	priv, pub := testKey(t)
	manifest := testManifest("2.0.0", channelStable)
	body, sigText := signed(t, priv, &manifest)
	if err := verifyManifestFile(body, sigText, pub); err != nil {
		t.Fatalf("in-memory manifest: %v", err)
	}
	path, sigPath := os.Getenv("DAMNBOT_RELEASE_MANIFEST"), os.Getenv("DAMNBOT_RELEASE_MANIFEST_SIG")
	if path == "" && sigPath == "" {
		return
	}
	fileBody := readTestFile(t, path)
	fileSig := readTestFile(t, sigPath)
	key, err := ParsePublicKey(os.Getenv("DAMNBOT_RELEASE_PUBKEY"))
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyManifestFile(fileBody, fileSig, key); err != nil {
		t.Fatalf("release manifest %s: %v", path, err)
	}
}

func readTestFile(t *testing.T, path string) []byte {
	t.Helper()
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := root.Close(); cerr != nil {
			t.Errorf("close root: %v", cerr)
		}
	}()
	data, err := root.ReadFile(filepath.Base(path))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func verifyManifestFile(body, sigText []byte, pub ed25519.PublicKey) error {
	sig, err := parseSignature(sigText)
	if err != nil {
		return err
	}
	if !ed25519.Verify(pub, body, sig) {
		return errors.New("signature is invalid")
	}
	_, err = parseManifest(body)
	return err
}

func formatSignature(sig []byte) []byte {
	return []byte(sigHeader + "\n" + base64.StdEncoding.EncodeToString(sig) + "\n")
}

func TestFetchRejectsInsecureURL(t *testing.T) {
	c := netx.New(netx.Config{}).Client("update", netx.Background)
	for _, raw := range []string{"http://example.test/releases", "https://u:p@example.test/releases", "file:///etc/passwd"} {
		if _, err := fetch(t.Context(), c, raw, 1024); err == nil {
			t.Errorf("fetch accepted %q", raw)
		}
	}
}
