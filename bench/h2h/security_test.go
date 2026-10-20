package h2h

import (
	"bufio"
	"compress/gzip"
	"encoding/base64"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestAgentIsolation (ADR-085): the agent runs with a different uid than the
// proxy. A user namespace is the cheap proof on Linux; when the environment
// cannot create one the test skips with the exact reason (never silently).
func TestAgentIsolation(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("unshare"); err != nil {
		t.Skip("user namespace unavailable: unshare not installed")
	}
	out, err := exec.Command("unshare", "-Ur", "id", "-u").Output()
	if err != nil {
		t.Skipf("user namespace unavailable: %v", err)
	}
	inside := strings.TrimSpace(string(out))
	if inside == "0" && os.Geteuid() == 0 {
		t.Skip("running as root: namespace uid 0 equals proxy uid; isolation is not observable")
	}
	if inside == strings.TrimSpace(os.Getenv("UID")) || inside == "" {
		t.Fatalf("namespace uid %q did not isolate from the proxy uid", inside)
	}
	for _, kv := range CleanEnv() {
		if strings.HasPrefix(kv, "H2H_KEY_FILE=") {
			t.Fatalf("agent env leaks H2H_KEY_FILE: %q", kv)
		}
	}
}

// TestRecordingsNoKey (ADR-085): every committed recording is free of generic
// token patterns and, when H2H_KEY_FILE is set, of the key in literal/base64/
// base64url/hex/12-char-prefix form.
func TestRecordingsNoKey(t *testing.T) {
	t.Parallel()
	secrets := keyForms(t)
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`sk-[A-Za-z0-9_-]{20,}`),
		regexp.MustCompile(`ghp_[A-Za-z0-9]{36}`),
		regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`),
		regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
		regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{10,}`),
		regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
		regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`),
	}
	files := recordingFiles(t)
	for _, path := range files {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		zr, err := gzip.NewReader(f)
		if err != nil {
			_ = f.Close()
			t.Fatalf("%s: %v", path, err)
		}
		sc := bufio.NewScanner(zr)
		sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
		for sc.Scan() {
			line := sc.Text()
			for _, p := range patterns {
				if p.MatchString(line) {
					t.Errorf("%s: generic token pattern %s", path, p)
				}
			}
			for _, s := range secrets {
				if s != "" && strings.Contains(line, s) {
					t.Errorf("%s: H2H_KEY_FILE value leaked", path)
				}
			}
		}
		if err := sc.Err(); err != nil {
			t.Errorf("%s: %v", path, err)
		}
		_ = zr.Close()
		_ = f.Close()
	}
}

func recordingFiles(t *testing.T) []string {
	t.Helper()
	var files []string
	if err := filepath.WalkDir("results", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".jsonl.gz") {
			files = append(files, path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return files
}

func keyForms(t *testing.T) []string {
	t.Helper()
	path := os.Getenv("H2H_KEY_FILE")
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("H2H_KEY_FILE: %v", err)
	}
	key := strings.TrimSpace(string(b))
	if key == "" {
		return nil
	}
	forms := []string{key}
	forms = append(forms, base64.StdEncoding.EncodeToString([]byte(key)))
	forms = append(forms, base64.RawURLEncoding.EncodeToString([]byte(key)))
	forms = append(forms, hex.EncodeToString([]byte(key)))
	if len(key) > 12 {
		forms = append(forms, key[:12])
	}
	return forms
}
