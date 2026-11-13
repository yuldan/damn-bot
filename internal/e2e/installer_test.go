package e2e

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/blake2b"
)

const installerVersion = "1.2.3"

// installerFixture is a signed fake release served over loopback HTTP plus an
// empty HOME. Nothing in the host HOME or config dirs is ever read or written.
type installerFixture struct {
	t       *testing.T
	root    string
	home    string
	data    string
	bin     string
	version string
	pub     string
	mu      sync.Mutex
	routes  map[string][]byte
	hits    map[string]int
	srv     *httptest.Server
	rc      map[string][]byte
}

func newInstallerFixture(t *testing.T) *installerFixture {
	t.Helper()
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	fx := &installerFixture{
		t:       t,
		root:    root,
		home:    t.TempDir(),
		version: installerVersion,
		rc:      map[string][]byte{},
		routes:  map[string][]byte{},
		hits:    map[string]int{},
	}
	fx.data = installStateDir(fx.home)
	fx.bin = filepath.Join(fx.home, "bin")
	for rel, body := range map[string]string{
		".bashrc":                  "# user bash\n",
		".zshrc":                   "# user zsh\n",
		".config/fish/config.fish": "# user fish\n",
	} {
		name := filepath.Join(fx.home, rel)
		if merr := os.MkdirAll(filepath.Dir(name), 0o700); merr != nil {
			t.Fatal(merr)
		}
		if werr := os.WriteFile(name, []byte(body), 0o600); werr != nil {
			t.Fatal(werr)
		}
		fx.rc[name] = []byte(body)
	}

	binName := filepath.Base(binPaths.main)
	tarball := tarArchive(t, binPaths.main, binName)
	zipped := zipArchive(t, binPaths.main, "damnbot.exe")
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	fx.pub = minisignPublicKey(pub)
	sumsTgz := fmt.Sprintf("%s  damnbot_%s_linux_amd64.tar.gz\n", shaHex(tarball), fx.version)
	sumsTgz += fmt.Sprintf("%s  damnbot_%s_linux_arm64.tar.gz\n", shaHex(tarball), fx.version)
	sumsTgz += fmt.Sprintf("%s  damnbot_%s_darwin_amd64.tar.gz\n", shaHex(tarball), fx.version)
	sumsTgz += fmt.Sprintf("%s  damnbot_%s_darwin_arm64.tar.gz\n", shaHex(tarball), fx.version)
	sumsZip := fmt.Sprintf("%s  damnbot_%s_windows_amd64.zip\n", shaHex(zipped), fx.version)
	sumsZip += fmt.Sprintf("%s  damnbot_%s_windows_arm64.zip\n", shaHex(zipped), fx.version)
	sums := []byte(sumsTgz + sumsZip)
	sig := minisignSignature(t, priv, pub, sums)

	prefix := "/v" + fx.version + "/"
	fx.put(prefix+"SHA256SUMS", sums)
	fx.put(prefix+"SHA256SUMS.minisig", sig)
	fx.put(prefix+"damnbot_"+fx.version+"_linux_amd64.tar.gz", tarball)
	fx.put(prefix+"damnbot_"+fx.version+"_windows_amd64.zip", zipped)
	fx.put(prefix+"damnbot_"+fx.version+"_darwin_arm64.tar.gz", tarball)
	fx.put(prefix+"damnbot_"+fx.version+"_darwin_amd64.tar.gz", tarball)
	fx.put(prefix+"damnbot_"+fx.version+"_linux_arm64.tar.gz", tarball)
	fx.put(prefix+"damnbot_"+fx.version+"_windows_arm64.zip", zipped)
	fx.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fx.mu.Lock()
		body, ok := fx.routes[r.URL.Path]
		fx.hits[r.URL.Path]++
		fx.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		if _, werr := w.Write(body); werr != nil {
			return
		}
	}))
	t.Cleanup(fx.srv.Close)
	return fx
}

// installStateDir mirrors install.sh's state directory: Linux honours
// XDG_DATA_HOME, Darwin always uses Application Support (XDG is ignored), and
// Windows uses install.ps1 (the POSIX installer skips there).
func installStateDir(home string) string {
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "damnbot")
	}
	return filepath.Join(home, ".local", "share", "damnbot")
}

// installAsset is the archive name install.sh selects for this runner.
func installAsset(version string) string {
	ext := ".tar.gz"
	if runtime.GOOS == "windows" {
		ext = ".zip"
	}
	return "damnbot_" + version + "_" + runtime.GOOS + "_" + runtime.GOARCH + ext
}

func (fx *installerFixture) put(path string, body []byte) {
	fx.mu.Lock()
	defer fx.mu.Unlock()
	fx.routes[path] = body
}

func (fx *installerFixture) env(extra ...string) []string {
	env := []string{
		"HOME=" + fx.home,
		"XDG_DATA_HOME=" + filepath.Join(fx.home, ".local", "share"),
		"XDG_CONFIG_HOME=" + filepath.Join(fx.home, ".config"),
		"TMPDIR=" + fx.home,
		"DAMNBOT_INSTALL_BASE_URL=" + fx.srv.URL,
		"DAMNBOT_VERSION=" + fx.version,
		"DAMNBOT_INSTALL_PUBKEY=" + fx.pub,
		"DAMNBOT_BIN_DIR=" + fx.bin,
		"PATH=" + os.Getenv("PATH"),
	}
	return append(env, extra...)
}

func (fx *installerFixture) install() Result {
	fx.t.Helper()
	cmd := exec.CommandContext(fx.t.Context(), "sh", filepath.Join(fx.root, "install.sh")) // #nosec G204 -- test-owned script path
	cmd.Env = fx.env()
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if ee, ok := errors.AsType[*exec.ExitError](err); ok {
		code = ee.ExitCode()
	} else if err != nil {
		fx.t.Fatalf("run install.sh: %v", err)
	}
	return Result{Stdout: out.String(), Stderr: errb.String(), Code: code}
}

func (fx *installerFixture) target() string { return filepath.Join(fx.bin, "damnbot") }
func (fx *installerFixture) manifest() string {
	return filepath.Join(fx.data, "install-manifest.json")
}

func (fx *installerFixture) assertNoWrites() {
	fx.t.Helper()
	if _, err := os.Stat(fx.target()); !os.IsNotExist(err) {
		fx.t.Fatalf("binary was written despite failure: %v", err)
	}
	if _, err := os.Stat(fx.manifest()); !os.IsNotExist(err) {
		fx.t.Fatalf("manifest was written despite failure: %v", err)
	}
	for name, want := range fx.rc {
		got, err := os.ReadFile(name) // #nosec G304 -- test temp rc path
		if err != nil {
			fx.t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			fx.t.Fatalf("%s changed despite failed verification:\n%s", name, got)
		}
	}
}

func TestInstallerRejectsTamper(t *testing.T) {
	requireInstallerTools(t)
	cases := map[string]func(*installerFixture){
		"archive byte changed": func(fx *installerFixture) {
			fx.mu.Lock()
			key := "/v" + fx.version + "/" + installAsset(fx.version)
			body := append([]byte(nil), fx.routes[key]...)
			fx.mu.Unlock()
			body[len(body)/2] ^= 0xff
			fx.put(key, body)
		},
		"checksum file changed": func(fx *installerFixture) {
			fx.mu.Lock()
			body := append([]byte(nil), fx.routes["/v"+fx.version+"/SHA256SUMS"]...)
			fx.mu.Unlock()
			body[0] ^= 0x01
			fx.put("/v"+fx.version+"/SHA256SUMS", body)
		},
		"signature from another key": func(fx *installerFixture) {
			otherPub, otherPriv, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				fx.t.Fatal(err)
			}
			fx.mu.Lock()
			sums := append([]byte(nil), fx.routes["/v"+fx.version+"/SHA256SUMS"]...)
			fx.mu.Unlock()
			fx.put("/v"+fx.version+"/SHA256SUMS.minisig", minisignSignature(fx.t, otherPriv, otherPub, sums))
		},
		"signature byte changed": func(fx *installerFixture) {
			fx.mu.Lock()
			body := append([]byte(nil), fx.routes["/v"+fx.version+"/SHA256SUMS.minisig"]...)
			fx.mu.Unlock()
			body[len(body)-2] ^= 0x01
			fx.put("/v"+fx.version+"/SHA256SUMS.minisig", body)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			fx := newInstallerFixture(t)
			mutate(fx)
			res := fx.install()
			if res.Code == 0 {
				t.Fatalf("installer accepted tampered release\nstdout=%s\nstderr=%s", res.Stdout, res.Stderr)
			}
			fx.assertNoWrites()
		})
	}
}

func TestInstallerVerifiesSignature(t *testing.T) {
	requireInstallerTools(t)
	fx := newInstallerFixture(t)
	res := fx.install()
	if res.Code != 0 {
		t.Fatalf("valid signed release rejected: %s\n%s", res.Stdout, res.Stderr)
	}
	fx.mu.Lock()
	hitSums, hitSig := fx.hits["/v"+fx.version+"/SHA256SUMS"], fx.hits["/v"+fx.version+"/SHA256SUMS.minisig"]
	fx.mu.Unlock()
	if hitSums == 0 || hitSig == 0 {
		t.Fatalf("installer did not fetch sums/signature: sums=%d sig=%d", hitSums, hitSig)
	}
	if _, err := os.Stat(fx.target()); err != nil {
		t.Fatalf("binary missing after verified install: %v", err)
	}
	fx.assertRCMarkers()
}

type installerManifest struct {
	Schema    int    `json:"schema"`
	Version   string `json:"version"`
	StateDir  string `json:"state_dir"`
	Installed string `json:"installed_at"`
	Files     []struct {
		Path         string  `json:"path"`
		Kind         string  `json:"kind"`
		SHA256Before *string `json:"sha256_before"`
		SHA256After  string  `json:"sha256_after"`
	} `json:"files"`
	Blocks []struct {
		Path         string `json:"path"`
		Begin        string `json:"begin"`
		End          string `json:"end"`
		AddedNewline bool   `json:"added_newline"`
	} `json:"blocks"`
	Dirs []string `json:"dirs"`
}

func TestInstallerManifest(t *testing.T) {
	requireInstallerTools(t)
	fx := newInstallerFixture(t)
	if res := fx.install(); res.Code != 0 {
		t.Fatalf("install: %s\n%s", res.Stdout, res.Stderr)
	}
	info, err := os.Stat(fx.manifest())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("manifest mode = %o, want 600", info.Mode().Perm())
	}
	raw, err := os.ReadFile(fx.manifest()) // #nosec G304 -- test temp path
	if err != nil {
		t.Fatal(err)
	}
	var m installerManifest
	if uerr := json.Unmarshal(raw, &m); uerr != nil {
		t.Fatalf("manifest json: %v\n%s", uerr, raw)
	}
	if m.Schema != 1 || m.Version != installerVersion || m.StateDir != fx.data {
		t.Fatalf("manifest header = %+v", m)
	}
	kinds := assertManifestFiles(t, fx, &m)
	if kinds["binary"] != 1 || kinds["rc"] != 3 {
		t.Fatalf("manifest kinds = %v", kinds)
	}
	assertManifestBlocks(t, fx, &m)
}

func assertManifestFiles(t *testing.T, fx *installerFixture, m *installerManifest) map[string]int {
	t.Helper()
	kinds := map[string]int{}
	for _, f := range m.Files {
		assertSafeInstallerPath(t, fx.home, f.Path)
		kinds[f.Kind]++
		got, err := os.ReadFile(f.Path) // #nosec G304 -- manifest path asserted under HOME
		if err != nil {
			t.Fatal(err)
		}
		if shaHex(got) != f.SHA256After {
			t.Fatalf("sha256_after mismatch for %s", f.Path)
		}
	}
	return kinds
}

func assertManifestBlocks(t *testing.T, fx *installerFixture, m *installerManifest) {
	t.Helper()
	if len(m.Blocks) != 3 {
		t.Fatalf("manifest blocks = %d, want 3", len(m.Blocks))
	}
	for _, b := range m.Blocks {
		assertSafeInstallerPath(t, fx.home, b.Path)
		if b.Begin != "# >>> damnbot >>>" || b.End != "# <<< damnbot <<<" {
			t.Fatalf("block markers = %q %q", b.Begin, b.End)
		}
	}
	for _, d := range m.Dirs {
		assertSafeInstallerPath(t, fx.home, d)
	}
}

func TestInstallerE2EVersion(t *testing.T) {
	requireInstallerTools(t)
	fx := newInstallerFixture(t)
	if res := fx.install(); res.Code != 0 {
		t.Fatalf("install: %s\n%s", res.Stdout, res.Stderr)
	}
	cmd := exec.CommandContext(t.Context(), fx.target(), "--version") // #nosec G204 -- test-built binary
	cmd.Env = fx.env()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("installed binary --version: %v\n%s", err, out)
	}
	if !strings.HasPrefix(string(out), "damnbot ") {
		t.Fatalf("--version = %q", out)
	}
}

func TestInstallerPowerShell(t *testing.T) {
	shell := powerShellShell(t)
	fx := newInstallerFixture(t)
	local := filepath.Join(fx.home, "appdata")
	env, hasVerifier := powerShellInstallerEnv(t, shell, fx, local)
	out, err := runPowerShellInstaller(t, shell, env)
	if !hasVerifier {
		if err == nil {
			t.Fatalf("install.ps1 succeeded without a signature verifier:\n%s", out)
		}
		if !bytes.Contains(out, []byte("verification requires minisign or openssl")) {
			t.Fatalf("install.ps1 failed for the wrong reason: %v\n%s", err, out)
		}
		if _, serr := os.Stat(filepath.Join(fx.bin, "damnbot.exe")); !os.IsNotExist(serr) {
			t.Fatalf("binary written without verification: %v", serr)
		}
		return
	}
	if err != nil {
		t.Fatalf("install.ps1: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(fx.bin, "damnbot.exe")); err != nil {
		t.Fatalf("PowerShell install missing binary: %v", err)
	}
	if _, err := os.Stat(filepath.Join(local, "damnbot", "install-manifest.json")); err != nil {
		t.Fatalf("PowerShell manifest missing: %v", err)
	}
}

func powerShellShell(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"pwsh", "powershell"} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	if isCI() {
		t.Fatal("no PowerShell interpreter in CI; install.ps1 path must be exercised")
	}
	t.Skip("no PowerShell interpreter (non-CI host)")
	return ""
}

func powerShellInstallerEnv(t *testing.T, shell string, fx *installerFixture, local string) ([]string, bool) {
	t.Helper()
	env := fx.env("LOCALAPPDATA="+local, "APPDATA="+filepath.Join(local, "Roaming"), "USERPROFILE="+fx.home)
	if runtime.GOOS == "windows" {
		for _, dir := range []string{
			`C:\Program Files\Git\ucrt64\bin`, `C:\Program Files\Git\mingw64\bin`,
			`C:\Program Files\Git\usr\bin`, `C:\Program Files\Git\cmd`,
			`C:\Program Files\OpenSSL-Win64\bin`,
		} {
			if _, serr := os.Stat(filepath.Join(dir, "openssl.exe")); serr == nil {
				env = prependPath(env, dir)
				break
			}
		}
	}
	probe := exec.CommandContext(t.Context(), shell, "-NoProfile", "-Command", "Get-Command openssl -CommandType Application | Select-Object -First 1") // #nosec G204 -- test-owned interpreter
	probe.Env = env
	out, err := probe.CombinedOutput()
	if err == nil && strings.TrimSpace(string(out)) != "" {
		return env, true
	}
	if !isCI() {
		t.Skipf("PowerShell-visible openssl unavailable: %v %s", err, out)
	}
	return env, false
}

func runPowerShellInstaller(t *testing.T, shell string, env []string) ([]byte, error) {
	t.Helper()
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), shell, "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", filepath.Join(root, "install.ps1")) // #nosec G204 -- test-owned script/interpreter
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("install.ps1: %w", err)
	}
	return out, nil
}

func isCI() bool { return os.Getenv("CI") != "" || os.Getenv("GITHUB_ACTIONS") == "true" }

func TestReleasePackagesMetadata(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("release packaging script is POSIX")
	}
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	dist := t.TempDir()
	version := "1.2.3"
	names := []string{
		"damnbot_1.2.3_darwin_amd64.tar.gz", "damnbot_1.2.3_darwin_arm64.tar.gz",
		"damnbot_1.2.3_linux_amd64.tar.gz", "damnbot_1.2.3_linux_arm64.tar.gz",
		"damnbot_1.2.3_windows_amd64.zip", "damnbot_1.2.3_windows_arm64.zip",
	}
	var sums strings.Builder
	for i, name := range names {
		fmt.Fprintf(&sums, "%064x  %s\n", i+1, name)
	}
	if werr := os.WriteFile(filepath.Join(dist, "SHA256SUMS"), []byte(sums.String()), 0o600); werr != nil {
		t.Fatal(werr)
	}
	if werr := os.WriteFile(filepath.Join(dist, "release.json"), []byte(`{"version":"`+version+`"}`), 0o600); werr != nil {
		t.Fatal(werr)
	}
	cmd := exec.CommandContext(t.Context(), "bash", filepath.Join(root, "scripts", "release-packages.sh"), dist) // #nosec G204 -- test-owned script path
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("release-packages: %v\n%s", err, out)
	}
	for _, rel := range []string{
		"packaging/damnbot.rb",
		"packaging/winget/DamnBot.DamnBot.yaml",
		"packaging/winget/DamnBot.DamnBot.installer.yaml",
		"packaging/deb/control-amd64",
		"packaging/rpm/damnbot-x86_64.spec",
	} {
		body, berr := os.ReadFile(filepath.Join(dist, rel)) // #nosec G304 -- test temp path
		if berr != nil {
			t.Fatal(berr)
		}
		if regexp.MustCompile(`@[A-Z_]+@`).Match(body) {
			t.Fatalf("%s still has placeholders:\n%s", rel, body)
		}
	}
	formula, err := os.ReadFile(filepath.Join(dist, "packaging", "damnbot.rb")) // #nosec G304 -- test temp path
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(formula, []byte(fmt.Sprintf("%064x", 3))) {
		t.Fatalf("formula does not carry the linux/amd64 checksum:\n%s", formula)
	}
}

func prependPath(env []string, dir string) []string {
	for i, kv := range env {
		if rest, ok := strings.CutPrefix(kv, "PATH="); ok {
			env[i] = "PATH=" + dir + string(os.PathListSeparator) + rest
			return env
		}
	}
	return append(env, "PATH="+dir)
}

func assertSafeInstallerPath(t *testing.T, home, p string) {
	t.Helper()
	if !filepath.IsAbs(p) || filepath.Clean(p) != p {
		t.Fatalf("manifest path is not canonical absolute: %q", p)
	}
	for part := range strings.SplitSeq(p, string(filepath.Separator)) {
		if part == ".." {
			t.Fatalf("manifest path escapes: %q", p)
		}
	}
	if p != home && !strings.HasPrefix(p, home+string(filepath.Separator)) {
		t.Fatalf("manifest path outside HOME: %q", p)
	}
}

func (fx *installerFixture) assertRCMarkers() {
	fx.t.Helper()
	for name := range fx.rc {
		body, err := os.ReadFile(name) // #nosec G304 -- test temp rc path
		if err != nil {
			fx.t.Fatal(err)
		}
		if !bytes.Contains(body, []byte("# >>> damnbot >>>")) || !bytes.Contains(body, []byte("# <<< damnbot <<<")) {
			fx.t.Fatalf("%s has no marker block:\n%s", name, body)
		}
	}
}

func requireInstallerTools(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("install.sh is POSIX")
	}
	for _, tool := range []string{"sh", "tar", "openssl"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available: %v", tool, err)
		}
	}
}

func minisignPublicKey(pub ed25519.PublicKey) string {
	id := keyID(pub)
	raw := append(append([]byte("ED"), id[:]...), pub...)
	return base64.StdEncoding.EncodeToString(raw)
}

func minisignSignature(t *testing.T, priv ed25519.PrivateKey, pub ed25519.PublicKey, data []byte) []byte {
	t.Helper()
	id := keyID(pub)
	digest := blake2b.Sum512(data)
	sig := ed25519.Sign(priv, digest[:])
	sigStruct := append(append([]byte("ED"), id[:]...), sig...)
	trusted := "timestamp:0\tfile:SHA256SUMS"
	global := ed25519.Sign(priv, append(append([]byte(nil), sig...), []byte(trusted)...))
	globalStruct := append(append([]byte("ED"), id[:]...), global...)
	var b bytes.Buffer
	fmt.Fprintf(&b, "untrusted comment: signature from minisign secret key\n%s\ntrusted comment: %s\n%s\n",
		base64.StdEncoding.EncodeToString(sigStruct), trusted, base64.StdEncoding.EncodeToString(globalStruct))
	return b.Bytes()
}

func keyID(pub ed25519.PublicKey) [8]byte {
	sum := sha256.Sum256(pub)
	var id [8]byte
	copy(id[:], sum[:8])
	return id
}

func shaHex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func tarArchive(t *testing.T, source, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(source) // #nosec G304 -- test-built binary path
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func zipArchive(t *testing.T, source, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(source) // #nosec G304 -- test-built binary path
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestF9UI018BootstrapInstaller(t *testing.T) {
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	shBody, err := os.ReadFile(filepath.Join(root, "install.sh")) // #nosec G304 -- repo file
	if err != nil {
		t.Fatal(err)
	}
	if lines := bytes.Count(shBody, []byte("\n")); lines > 150 {
		t.Fatalf("install.sh = %d lines, want <= 150", lines)
	}
	if _, err := os.Stat(filepath.Join(root, "install.ps1")); err != nil {
		t.Fatalf("install.ps1 missing: %v", err)
	}
	for _, banned := range []string{"tauri", "cargo build", "npm install", "electron"} {
		if bytes.Contains(bytes.ToLower(shBody), []byte(banned)) {
			t.Fatalf("install.sh references %q; UI-018 is a script installer", banned)
		}
	}
}

func TestF9OPS032InstallerScripts(t *testing.T) {
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	shPath := filepath.Join(root, "install.sh")
	shBody, err := os.ReadFile(shPath) // #nosec G304 -- repo file
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(shBody, []byte("\n")) > 150 {
		t.Fatalf("install.sh exceeds 150 lines")
	}
	if bytes.Contains(shBody, []byte("| bash")) || bytes.Contains(shBody, []byte("| sh")) {
		t.Fatalf("install.sh must not document or use pipe-to-shell")
	}
	if err := exec.CommandContext(t.Context(), "sh", "-n", shPath).Run(); err != nil { // #nosec G204 -- repo script
		t.Fatalf("install.sh syntax: %v", err)
	}
	if info, err := os.Stat(filepath.Join(root, "install.ps1")); err != nil || info.Size() == 0 {
		t.Fatalf("install.ps1 missing/empty: %v", err)
	}
}

func TestInstallerDocsNoAVExclusion(t *testing.T) {
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	check := func(path string, body []byte) {
		for _, banned := range []string{"add-mppreference", "set-mppreference", "exclusionpath"} {
			if bytes.Contains(bytes.ToLower(body), []byte(banned)) {
				t.Fatalf("%s contains antivirus exclusion instruction %q", path, banned)
			}
		}
	}
	readme, rerr := os.ReadFile(filepath.Join(root, "README.md")) // #nosec G304 -- repo file
	if rerr == nil {
		check("README.md", readme)
	}
	entries, derr := os.ReadDir(filepath.Join(root, "docs"))
	if derr != nil {
		t.Fatal(derr)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		body, rerr := os.ReadFile(filepath.Join(root, "docs", e.Name())) // #nosec G304 -- repo docs
		if rerr != nil {
			t.Fatal(rerr)
		}
		check(filepath.Join("docs", e.Name()), body)
	}
}
