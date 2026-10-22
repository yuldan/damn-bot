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

// TestRecordingsNoKey (ADR-085, SEC-review): every committed artifact under
// results/ is free of generic token patterns and, when H2H_KEY_FILE is set, of
// the key in literal/base64/base64url/hex/12-char-prefix form. The guard used
// to walk only *.jsonl.gz, so summary.json — which records the command lines of
// a run — was never scanned and a real key literal shipped with #134.
func TestRecordingsNoKey(t *testing.T) {
	t.Parallel()
	secrets := keyForms(t)
	patterns := tokenPatterns()
	for _, path := range resultsFiles(t) {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		var sc *bufio.Scanner
		var closer func() error
		if strings.HasSuffix(path, ".gz") {
			zr, zerr := gzip.NewReader(f)
			if zerr != nil {
				_ = f.Close()
				t.Fatalf("%s: %v", path, zerr)
			}
			sc, closer = bufio.NewScanner(zr), zr.Close
		} else {
			sc, closer = bufio.NewScanner(f), func() error { return nil }
		}
		sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
		lineNo := 0
		for sc.Scan() {
			lineNo++
			line := sc.Text()
			for _, p := range patterns {
				if p.MatchString(line) {
					t.Errorf("%s:%d: generic token pattern %s", path, lineNo, p)
				}
			}
			for _, s := range secrets {
				if s != "" && strings.Contains(line, s) {
					t.Errorf("%s:%d: H2H_KEY_FILE value leaked", path, lineNo)
				}
			}
		}
		if err := sc.Err(); err != nil {
			t.Errorf("%s: %v", path, err)
		}
		_ = closer()
		_ = f.Close()
	}
}

// tokenPatterns are the generic credential shapes a committed bench artifact
// must never contain; the redaction tests assert against the same list.
func tokenPatterns() []*regexp.Regexp {
	return []*regexp.Regexp{
		regexp.MustCompile(`sk-[A-Za-z0-9_-]{20,}`),
		regexp.MustCompile(`ghp_[A-Za-z0-9]{36}`),
		regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`),
		regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
		regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{10,}`),
		regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
		regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`),
	}
}

// resultsFiles lists every regular file under results/, not only the
// recordings: summary.json carries the command lines of a run.
func resultsFiles(t *testing.T) []string {
	t.Helper()
	var files []string
	if err := filepath.WalkDir("results", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && !strings.HasSuffix(path, ".lock") {
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
