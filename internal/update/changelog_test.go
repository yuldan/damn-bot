package update

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/netx"
)

const sampleChangelog = `# Changelog

## Unreleased

- kerja berikutnya

## [2.0.0] - 2026-12-01

### Breaking

- format config baru

### Migrations

- 0015_new_table

### Security

- none

## [1.2.0] - 2026-11-01

### Breaking

- none

### Migrations

- none

### Security

- CVE-2026-0001

## [1.1.0] - 2026-10-01

### Breaking

- none

### Migrations

- none

### Security

- none
`

func TestSliceChangelog(t *testing.T) {
	t.Parallel()
	got := sliceChangelog(sampleChangelog, "1.1.0", "2.0.0")
	for _, want := range []string{"## [2.0.0]", "## [1.2.0]", "CVE-2026-0001"} {
		if !strings.Contains(got, want) {
			t.Errorf("slice lacks %q:\n%s", want, got)
		}
	}
	for _, notWant := range []string{"## [1.1.0]", "Unreleased", "kerja berikutnya"} {
		if strings.Contains(got, notWant) {
			t.Errorf("slice contains %q:\n%s", notWant, got)
		}
	}
	if one := sliceChangelog(sampleChangelog, "1.2.0", "2.0.0"); strings.Contains(one, "## [1.2.0]") || !strings.Contains(one, "## [2.0.0]") {
		t.Errorf("installed 1.2.0 slice:\n%s", one)
	}
	legacy := "- rilis lama\n"
	if got := sliceChangelog(legacy, "1.0.0", "2.0.0"); got != legacy {
		t.Errorf("legacy changelog = %q, want unchanged", got)
	}
	if got := sliceChangelog(sampleChangelog, "devel", "2.0.0"); got != sampleChangelog {
		t.Errorf("non-semver installed version did not keep the text")
	}
}

func TestChangelogEntrySections(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		text string
		ver  string
		mig  []string
		ok   bool
	}{
		{"valid", sampleChangelog, "2.0.0", []string{"0015"}, true},
		{"missing security", "## [2.0.0]\n\n### Breaking\nnone\n\n### Migrations\nnone\n", "2.0.0", nil, false},
		{"missing migrations", "## [2.0.0]\n\n### Breaking\nnone\n\n### Security\nnone\n", "2.0.0", nil, false},
		{"unnamed migration", sampleChangelog, "2.0.0", []string{"0016"}, false},
		{"none is enough", "## [2.0.0]\n\n### Breaking\nnone\n\n### Migrations\nnone\n\n### Security\nnone\n", "2.0.0", nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := checkReleaseEntry(tc.text, tc.ver, tc.mig)
			if (err == nil) != tc.ok {
				t.Fatalf("checkReleaseEntry = %v, want ok=%t", err, tc.ok)
			}
		})
	}
}

// checkReleaseEntry enforces GAP-INF-04: Breaking, Migrations and Security in
// every version entry, and every migration introduced by that release named
// under Migrations.
func checkReleaseEntry(text, version string, newMigrations []string) error {
	body := changelogBody(text, version)
	if body == "" {
		return &entryError{"missing version entry " + version}
	}
	for _, section := range []string{"Breaking", "Migrations", "Security"} {
		if !hasSubsection(body, section) {
			return &entryError{version + " lacks ### " + section}
		}
	}
	migrations := subsection(body, "Migrations")
	for _, number := range newMigrations {
		if !strings.Contains(migrations, number) {
			return &entryError{version + " Migrations does not name " + number}
		}
	}
	return nil
}

type entryError struct{ msg string }

func (e *entryError) Error() string { return e.msg }

// TestChangelogPerTag (GAP-INF-04): every git tag has an entry, every version
// entry has the three required sections, and new migrations are named. With no
// tags (PR CI) it still validates the structure of any version entry.
func TestChangelogPerTag(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..")
	text := readChangelog(t, root)
	validateExistingEntries(t, text)
	if want := os.Getenv("DAMNBOT_CHANGELOG_TAG"); want != "" {
		if verr := checkReleaseEntry(text, strings.TrimPrefix(want, "v"), nil); verr != nil {
			t.Fatalf("release tag: %v", verr)
		}
	}
	tags, err := gitLines(t, root, "tag", "--list", "v*", "--sort=version:refname")
	if err != nil || len(tags) == 0 {
		t.Logf("no git tags available: %v (structure checked only)", err)
		return
	}
	prev := ""
	for _, tag := range tags {
		checkTag(t, root, text, tag, prev)
		prev = tag
	}
	checkHeadMigrations(t, root, text, prev)
}

func readChangelog(t *testing.T, root string) string {
	t.Helper()
	data, err := fs.ReadFile(os.DirFS(root), "CHANGELOG.md")
	if err != nil {
		t.Fatalf("read CHANGELOG.md: %v", err)
	}
	return string(data)
}

func validateExistingEntries(t *testing.T, text string) {
	t.Helper()
	for _, section := range parseChangelog(text) {
		if section.version == "" {
			continue
		}
		if err := checkReleaseEntry(text, section.version, nil); err != nil {
			t.Errorf("%v", err)
		}
	}
}

func checkTag(t *testing.T, root, text, tag, prev string) {
	t.Helper()
	version := strings.TrimPrefix(tag, "v")
	atTag, err := migrationNumbers(t, root, tag)
	if err != nil {
		t.Fatalf("%s: %v", tag, err)
	}
	if err := checkReleaseEntry(text, version, migrationDelta(t, root, atTag, prev)); err != nil {
		t.Errorf("%s: %v", tag, err)
	}
}

// migrationDelta returns the migrations introduced after prev; the first
// release has no baseline and therefore no "new" migrations.
func migrationDelta(t *testing.T, root string, atTag []string, prev string) []string {
	t.Helper()
	if prev == "" {
		return nil
	}
	prevList, err := migrationNumbers(t, root, prev)
	if err != nil {
		t.Fatalf("%s: %v", prev, err)
	}
	seen := map[string]bool{}
	for _, n := range prevList {
		seen[n] = true
	}
	var added []string
	for _, n := range atTag {
		if !seen[n] {
			added = append(added, n)
		}
	}
	return added
}

func checkHeadMigrations(t *testing.T, root, text, prev string) {
	t.Helper()
	head, err := migrationNumbers(t, root, "HEAD")
	if err != nil {
		t.Logf("HEAD migrations unavailable: %v", err)
		return
	}
	added := migrationDelta(t, root, head, prev)
	if len(added) == 0 {
		return
	}
	body := changelogBody(text, "")
	if body == "" {
		return
	}
	migrations := subsection(body, "Migrations")
	for _, n := range added {
		if !strings.Contains(migrations, n) {
			t.Errorf("Unreleased Migrations does not name new migration %s", n)
		}
	}
}

func gitLines(t *testing.T, root string, args ...string) ([]string, error) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...) // #nosec G204 -- fixed git arguments from this test
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	var lines []string
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines, nil
}

func migrationNumbers(t *testing.T, root, rev string) ([]string, error) {
	t.Helper()
	lines, err := gitLines(t, root, "ls-tree", "-r", "--name-only", rev, "internal/ctxdb/migrations")
	if err != nil {
		return nil, err
	}
	var nums []string
	for _, line := range lines {
		base := filepath.Base(line)
		if len(base) >= 4 && strings.HasSuffix(base, ".sql") {
			nums = append(nums, base[:4])
		}
	}
	return nums, nil
}

// TestUpdateCheckShowsChangelog (GAP-INF-04): the signed manifest carries the
// version entries and Check returns the slice between installed and target.
func TestUpdateCheckShowsChangelog(t *testing.T) {
	t.Parallel()
	priv, pub := testKey(t)
	man := testManifest("2.0.0", channelStable)
	man.Changelog = sampleChangelog
	body, sig := signed(t, priv, &man)
	srv := serveFeed(t, feedFixture{
		releases: []ghRelease{{TagName: "v2.0.0", Assets: []ghAsset{{Name: manifestAsset}, {Name: manifestSigAsset}}}},
		files:    map[string][]byte{manifestAsset: body, manifestSigAsset: sig},
	})
	version, changelog, available, err := Check(t.Context(), netx.New(netx.Config{}).Client("update", netx.Background),
		srv.URL+"/releases", channelStable, "1.1.0", pub)
	if err != nil || !available || version != "2.0.0" {
		t.Fatalf("Check = %q/%t/%v", version, available, err)
	}
	if !strings.Contains(changelog, "## [2.0.0]") || !strings.Contains(changelog, "## [1.2.0]") {
		t.Errorf("changelog lacks the installed→target slice:\n%s", changelog)
	}
	if strings.Contains(changelog, "## [1.1.0]") || strings.Contains(changelog, "Unreleased") {
		t.Errorf("changelog includes an out-of-range entry:\n%s", changelog)
	}
}

// TestChangelogPerTagSyntheticHistory proves the tag/migration path on a real
// (temporary) git history: two tags, one new migration in the second, and the
// entry must name it.
func TestChangelogPerTagSyntheticHistory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q")
	gitRun(t, dir, "config", "user.email", "test@example.invalid")
	gitRun(t, dir, "config", "user.name", "Test")
	if err := os.MkdirAll(filepath.Join(dir, "internal", "ctxdb", "migrations"), 0o750); err != nil {
		t.Fatal(err)
	}
	writeRepoFile(t, dir, "internal/ctxdb/migrations/0001_a.sql", "select 1;\n")
	writeRepoFile(t, dir, "CHANGELOG.md", "## [1.0.0] - 2026-01-01\n\n### Breaking\nnone\n\n### Migrations\n0001\n\n### Security\nnone\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "one")
	gitRun(t, dir, "tag", "v1.0.0")

	writeRepoFile(t, dir, "internal/ctxdb/migrations/0002_b.sql", "select 2;\n")
	writeRepoFile(t, dir, "CHANGELOG.md", "## [1.1.0] - 2026-02-01\n\n### Breaking\nnone\n\n### Migrations\n0002\n\n### Security\nnone\n\n## [1.0.0] - 2026-01-01\n\n### Breaking\nnone\n\n### Migrations\n0001\n\n### Security\nnone\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "two")
	gitRun(t, dir, "tag", "v1.1.0")

	text := readChangelog(t, dir)
	tags, err := gitLines(t, dir, "tag", "--list", "v*", "--sort=version:refname")
	if err != nil || len(tags) != 2 {
		t.Fatalf("tags = %q, %v", tags, err)
	}
	checkTag(t, dir, text, "v1.0.0", "")
	checkTag(t, dir, text, "v1.1.0", "v1.0.0")
	atTag, err := migrationNumbers(t, dir, "v1.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if added := migrationDelta(t, dir, atTag, "v1.0.0"); len(added) != 1 || added[0] != "0002" {
		t.Fatalf("migration delta = %q, want [0002]", added)
	}
	bad := "## [1.1.0] - 2026-02-01\n\n### Breaking\nnone\n\n### Migrations\nnone\n\n### Security\nnone\n"
	if err := checkReleaseEntry(bad, "1.1.0", []string{"0002"}); err == nil {
		t.Fatal("entry with a new migration and Migrations: none was accepted")
	}
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...) // #nosec G204 -- fixed git arguments from this test
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func writeRepoFile(t *testing.T, dir, rel, data string) {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}
