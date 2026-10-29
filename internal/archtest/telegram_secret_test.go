package archtest

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// telegramTokenShape is the Bot API token shape: an 8–10 digit bot id, a
// colon, then 35 characters of the base64url alphabet.
const telegramTokenShape = `[0-9]{8,10}:[A-Za-z0-9_-]{35}` // #nosec G101 -- a regex describing the shape, not a credential

// telegramScanExempt skips the gitleaks corpus under internal/redact/testdata:
// it deliberately carries synthetic samples of every secret family so the
// redactor's rules can be tested. Real credentials never live there.
func telegramScanExempt(p string) bool {
	return strings.HasPrefix(p, "internal/redact/testdata/gitleaks-") && strings.HasSuffix(p, ".tsv")
}

// telegramTokenLines returns the 1-based line numbers of every Bot API token
// shape in data; the token text itself is never returned or logged.
func telegramTokenLines(re *regexp.Regexp, data []byte) []int {
	var lines []int
	for _, loc := range re.FindAllIndex(data, -1) {
		lines = append(lines, bytes.Count(data[:loc[0]], []byte("\n"))+1)
	}
	return lines
}

// TestNoTelegramBotTokens fails when any tracked file (plus untracked files
// that git would commit) carries a Telegram Bot API token. Credentials belong
// in ~/.config/damnbot/telegram.env (0600), never in the repository.
func TestNoTelegramBotTokens(t *testing.T) {
	t.Parallel()
	re := regexp.MustCompile(telegramTokenShape)

	// Negative control: a synthetic value with the exact shape must be caught,
	// and a 34-character suffix must not. Both are assembled at run time so
	// this file stays clean for its own scan.
	synthetic := "12345678:" + strings.Repeat("Q", 35)
	if got := telegramTokenLines(re, []byte("prefix\n"+synthetic+"\n")); len(got) != 1 || got[0] != 2 {
		t.Fatalf("negative control: synthetic token matched at lines %v, want [2]", got)
	}
	near := "12345678:" + strings.Repeat("Q", 34)
	if got := telegramTokenLines(re, []byte(near)); len(got) != 0 {
		t.Fatalf("negative control: 34-character suffix matched at lines %v, want none", got)
	}

	// --others --exclude-standard extends the tracked set with files the next
	// commit would add, so a leak is caught before it reaches history too.
	out := runRepo(t, exec.CommandContext(t.Context(), "git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"))
	scanned := 0
	for p := range strings.SplitSeq(strings.TrimSuffix(string(out), "\x00"), "\x00") {
		if p == "" || telegramScanExempt(p) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(repoDir(), filepath.FromSlash(p)))
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		scanned++
		for _, line := range telegramTokenLines(re, raw) {
			t.Errorf("%s:%d: Telegram Bot API token shape found; keep credentials in ~/.config/damnbot/telegram.env (0600) and rotate any committed token", p, line)
		}
	}
	if scanned == 0 {
		t.Fatal("no files scanned")
	}
}
