package hub

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/skills"
)

// approvedHub is a hub over a fake store already holding one quarantined
// skill, with a named owner. The index and skill bytes come from freshFetch.
func approvedHub(t *testing.T, st *fakeStore, fetch mapFetcher) *Hub {
	t.Helper()
	return newTestHub(t, fetch, st, Config{Owner: "alice"})
}

func freshFetch(t *testing.T) mapFetcher {
	t.Helper()
	return mapFetcher{
		"https://skills.nousresearch.com/index.json":                   indexDoc(t, entryMap("findmy", skillBody, len(skillBody))),
		"https://skills.nousresearch.com/skills/apple/findmy/SKILL.md": skillBody,
	}
}

// TestF8SkillHubApproveOwner proves approval is owner-only and closed when no
// owner is configured.
func TestF8SkillHubApproveOwner(t *testing.T) {
	t.Parallel()
	st := &fakeStore{}
	h := approvedHub(t, st, freshFetch(t))
	if _, err := h.Download(t.Context(), SourceOfficial, new(mustEntry(t, freshFetch(t)))); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if err := h.Approve(t.Context(), "", 7); !errors.Is(err, ErrNotOwner) {
		t.Errorf("empty caller = %v, want ErrNotOwner", err)
	}
	if err := h.Approve(t.Context(), "mallory", 7); !errors.Is(err, ErrNotOwner) {
		t.Errorf("wrong caller = %v, want ErrNotOwner", err)
	}
	if st.approved {
		t.Fatal("wrong owner approved the skill")
	}
	if err := h.Approve(t.Context(), "alice", 7); err != nil {
		t.Fatalf("owner approve: %v", err)
	}
	if !st.approved {
		t.Fatal("owner approve did not reach the store")
	}
	closed := newTestHub(t, mapFetcher{}, &fakeStore{}, Config{})
	if err := closed.Approve(t.Context(), "alice", 7); !errors.Is(err, ErrNotOwner) {
		t.Errorf("owner-less hub = %v, want ErrNotOwner", err)
	}
}

// TestF8SkillHubInstallAtomic proves: 0 writes before approval, exactly one
// complete file after approval, and no overwrite on a second install.
func TestF8SkillHubInstallAtomic(t *testing.T) {
	t.Parallel()
	body := skillBody
	st := &fakeStore{}
	h := approvedHub(t, st, freshFetch(t))
	dir := t.TempDir()
	if _, err := h.Install(t.Context(), SourceOfficial, "findmy", dir); !errors.Is(err, ErrNotApproved) {
		t.Fatalf("unapproved Install = %v, want ErrNotApproved", err)
	}
	if files := listFiles(t, dir); len(files) != 0 {
		t.Fatalf("unapproved install wrote %v", files)
	}
	if _, err := h.Download(t.Context(), SourceOfficial, new(mustEntry(t, freshFetch(t)))); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if err := h.Approve(t.Context(), "alice", 7); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	path, err := h.Install(t.Context(), SourceOfficial, "findmy", dir)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	got, err := os.ReadFile(path) //nolint:gosec // path is inside the test's temp dir
	if err != nil {
		t.Fatalf("read installed skill: %v", err)
	}
	if string(got) != body {
		t.Errorf("installed bytes differ:\\n%s", got)
	}
	info, serr := os.Stat(path)
	if serr != nil {
		t.Fatalf("stat installed skill: %v", serr)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode())
	}
	if _, err := h.Install(t.Context(), SourceOfficial, "findmy", dir); !errors.Is(err, ErrInstalled) {
		t.Errorf("second Install = %v, want ErrInstalled", err)
	}
}

// TestF8SkillHubInstallNegative is the negative control set: a bad hash, a
// traversal name, and a rejected skill write zero files.
func TestF8SkillHubInstallNegative(t *testing.T) {
	t.Parallel()
	body := skillBody
	st := &fakeStore{}
	h := approvedHub(t, st, freshFetch(t))
	dir := t.TempDir()
	if _, err := h.Install(t.Context(), SourceOfficial, "../evil", dir); !errors.Is(err, ErrIndex) {
		t.Errorf("traversal name = %v, want ErrIndex", err)
	}
	if _, err := h.Download(t.Context(), SourceOfficial, new(mustEntry(t, freshFetch(t)))); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if err := h.Approve(t.Context(), "alice", 7); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	fetch := freshFetch(t)
	st2 := &fakeStore{}
	h2 := approvedHub(t, st2, fetch)
	if _, err := h2.Download(t.Context(), SourceOfficial, new(mustEntry(t, fetch))); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if err := h2.Approve(t.Context(), "alice", 7); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	bad := entryMap("findmy", body, len(body))
	bad["sha256"] = strings.Repeat("0", 64)
	fetch["https://skills.nousresearch.com/index.json"] = indexDoc(t, bad)
	if _, err := h2.Install(t.Context(), SourceOfficial, "findmy", dir); !errors.Is(err, ErrHash) {
		t.Errorf("bad hash = %v, want ErrHash", err)
	}
	if files := listFiles(t, dir); len(files) != 0 {
		t.Fatalf("negative installs wrote %v", files)
	}
}

// TestF8SkillHubSync proves a source sync quarantines every entry and that the
// optional sync installs only optional-skills entries.
func TestF8SKILL017OptionalSync(t *testing.T) {
	t.Parallel()
	body := skillBody
	bundled := entryMap("findmy", body, len(body))
	alphaBody := strings.Replace(body, "name: findmy", "name: alpha", 1)
	optional := entryMap("alpha", alphaBody, len(alphaBody))
	optional["path"] = "optional-skills/creative/alpha/SKILL.md"
	fetch := mapFetcher{
		"https://skills.nousresearch.com/index.json":                              indexDoc(t, bundled, optional),
		"https://skills.nousresearch.com/skills/apple/findmy/SKILL.md":            body,
		"https://skills.nousresearch.com/optional-skills/creative/alpha/SKILL.md": alphaBody,
	}
	st := &fakeStore{}
	h := approvedHub(t, st, fetch)
	res, err := h.SyncOptional(t.Context(), SourceOfficial)
	if err != nil {
		t.Fatalf("SyncOptional: %v", err)
	}
	if len(res.Installed) != 1 || res.Installed[0] != "alpha" {
		t.Fatalf("SyncOptional installed %v", res.Installed)
	}
	if st.approved || len(st.quarantined) != 1 {
		t.Fatalf("sync approved=%v quarantined=%d", st.approved, len(st.quarantined))
	}
	st2 := &fakeStore{}
	h2 := approvedHub(t, st2, fetch)
	res, err = h2.Sync(t.Context(), SourceOfficial)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(res.Installed) != 2 || len(st2.quarantined) != 2 {
		t.Fatalf("Sync installed=%v quarantined=%d", res.Installed, len(st2.quarantined))
	}
	empty := approvedHub(t, &fakeStore{}, mapFetcher{
		"https://skills.nousresearch.com/index.json": indexDoc(t, bundled),
	})
	if _, err := empty.SyncOptional(t.Context(), SourceOfficial); !errors.Is(err, ErrNoEntry) {
		t.Errorf("optional sync without optional entries = %v, want ErrNoEntry", err)
	}
}

// TestF8SKILL020UsageTracking proves the read-only aggregate: counts by action,
// no body/path/prompt content, and a missing ledger is empty.
func TestF8SKILL020UsageTracking(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	want := []struct {
		name   string
		action string
		at     int64
	}{
		{"beta", "indexed", 5},
		{"alpha", "quarantined", 2},
		{"alpha", "approved", 4},
		{"alpha", "rejected", 3},
		{"alpha", "indexed", 1},
		{"alpha", "other", 6},
	}
	for _, e := range want {
		if err := skills.AppendLedger(dir, &skills.LedgerEntry{
			At: e.at, Name: e.name, SHA256: strings.Repeat("a", 64),
			Path: "/secret/prompt-body", Source: "hub", Action: e.action,
		}); err != nil {
			t.Fatalf("AppendLedger: %v", err)
		}
	}
	rep, err := ReadUsage(dir)
	if err != nil {
		t.Fatalf("ReadUsage: %v", err)
	}
	if rep.Total != len(want) || len(rep.Entries) != 2 {
		t.Fatalf("total=%d entries=%d", rep.Total, len(rep.Entries))
	}
	checkAlphaUsage(t, rep.Entries[0])
	if text := fmt.Sprintf("%+v", rep); strings.Contains(text, "secret") || strings.Contains(text, "prompt") {
		t.Errorf("usage report leaked content: %s", text)
	}
	empty, err := ReadUsage(t.TempDir())
	if err != nil || empty.Total != 0 || len(empty.Entries) != 0 {
		t.Errorf("empty usage = %+v, %v", empty, err)
	}
}

// TestF8SkillHubUninstall proves removal, the missing-directory error, and the
// rejection of a path-shaped name.
func TestF8SkillHubUninstall(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "findmy"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Uninstall(dir, "findmy"); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "findmy")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("skill directory still present: %v", err)
	}
	if err := Uninstall(dir, "findmy"); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("missing skill = %v, want ErrNotInstalled", err)
	}
	if err := Uninstall(dir, "../findmy"); !errors.Is(err, ErrIndex) {
		t.Errorf("traversal name = %v, want ErrIndex", err)
	}
}

func checkAlphaUsage(t *testing.T, u UsageEntry) {
	t.Helper()
	if u.Name != "alpha" || u.Indexed != 1 || u.Quarantined != 1 ||
		u.Approved != 1 || u.Rejected != 1 || u.Other != 1 || u.LastAt != 6 {
		t.Errorf("alpha row = %+v", u)
	}
}

func listFiles(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return out
}
