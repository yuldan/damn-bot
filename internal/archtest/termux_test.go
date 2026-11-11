package archtest

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"mvdan.cc/sh/v3/syntax"
)

// mdLink matches Markdown inline links; images are matched too and checked the
// same way because an offline document must not depend on a remote asset.
var mdLink = regexp.MustCompile(`\]\(([^)\s]+)\)`)

// TestTermuxScriptSyntax (OPS-031, CROSS-CUTTING §7) parses
// scripts/termux-smoke.sh with the shell parser already used by cron (no bash
// binary needed on Windows/macOS) and checks the Termux contract: opt-in, no
// root, static android/arm64 build, and no privileged/container escape hatch.
func TestTermuxScriptSyntax(t *testing.T) {
	t.Parallel()
	const name = "scripts/termux-smoke.sh"
	script := readRepoFile(t, name)
	if _, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(script), name); err != nil {
		t.Fatalf("%s bukan bash valid: %v", name, err)
	}
	first, _, _ := strings.Cut(script, "\n")
	if strings.TrimSuffix(first, "\r") != "#!/usr/bin/env bash" {
		t.Errorf("%s: shebang pertama harus #!/usr/bin/env bash", name)
	}
	for _, want := range []string{
		"set -euo pipefail",
		"DAMNBOT_TERMUX_SMOKE",
		"id -u",
		"CGO_ENABLED=0",
		"GOOS=android",
		"GOARCH=arm64",
		"-tags goolm",
		"go build",
		"go test",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("%s tidak memuat %q", name, want)
		}
	}
	for _, forbidden := range []string{"sudo", "--privileged", "systemctl", "apt-get", "docker "} {
		if strings.Contains(script, forbidden) {
			t.Errorf("%s memuat pola terlarang %q (Termux tanpa root/daemon)", name, forbidden)
		}
	}
	if runtime.GOOS != "windows" { // checkout Windows tidak mewarisi bit POSIX
		info, err := os.Stat(filepath.Join(repoDir(), name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if info.Mode()&0o111 == 0 {
			t.Errorf("%s tidak executable", name)
		}
	}
}

// TestDocsLinksOffline checks the new Termux guide without any network access:
// every relative link resolves to a file in the repo, every #anchor exists in
// its target document, and http:// is rejected (the docs are offline-first).
func TestDocsLinksOffline(t *testing.T) {
	t.Parallel()
	checkDocLinksOffline(t, "docs/TERMUX.md")
}

func checkDocLinksOffline(t *testing.T, doc string) {
	t.Helper()
	text := readRepoFile(t, doc)
	for _, m := range mdLink.FindAllStringSubmatch(text, -1) {
		checkDocLink(t, doc, strings.TrimSpace(strings.Trim(m[1], "<>")))
	}
	if !strings.Contains(text, "OPS-031") || !strings.Contains(text, "termux-smoke.sh") {
		t.Error("docs/TERMUX.md tidak merujuk OPS-031 dan scripts/termux-smoke.sh")
	}
}

// checkDocLink validates one link target without network access.
func checkDocLink(t *testing.T, doc, target string) {
	t.Helper()
	if target == "" {
		return
	}
	if strings.HasPrefix(target, "http://") {
		t.Errorf("%s: tautan http:// tidak offline-safe: %s", doc, target)
		return
	}
	if strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
		return // https/mailto sengaja tidak di-fetch offline
	}
	path, anchor, _ := strings.Cut(target, "#")
	file := doc
	if path != "" {
		file = filepath.Clean(filepath.Join(filepath.Dir(doc), path))
		if strings.HasPrefix(file, "..") {
			t.Errorf("%s: tautan keluar repo: %s", doc, target)
			return
		}
		if _, err := os.Stat(filepath.Join(repoDir(), file)); err != nil {
			t.Errorf("%s: tautan rusak %s: %v", doc, target, err)
			return
		}
	}
	if anchor != "" && !docAnchors(t, file)[anchor] {
		t.Errorf("%s: anchor #%s tidak ada di %s", doc, anchor, file)
	}
}

// docAnchors builds the GitHub-style heading slugs of one Markdown document.
func docAnchors(t *testing.T, doc string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for line := range strings.SplitSeq(readRepoFile(t, doc), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "#") {
			continue
		}
		out[headingSlug(trimmed)] = true
	}
	return out
}

func headingSlug(line string) string {
	line = strings.TrimSpace(strings.TrimLeft(line, "#"))
	var b strings.Builder
	for _, r := range strings.ToLower(line) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('-')
		}
	}
	return b.String()
}
