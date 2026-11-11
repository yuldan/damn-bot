//go:build !minimal

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/skills"
)

func seedJourney(t *testing.T, data string) {
	t.Helper()
	now := time.Now().Add(-time.Hour).Unix()
	rows := []skills.LedgerEntry{
		{At: now - 30, Name: "alpha", SHA256: "a1", Source: "/src/alpha", Action: "indexed"},
		{At: now - 30, Name: "alpha", SHA256: "a1", Source: "/src/alpha", Action: "indexed"},
		{At: now - 10, Name: "beta", SHA256: "b1", Source: "", Action: "approved"},
	}
	for i := range rows {
		if err := skills.AppendLedger(data, &rows[i]); err != nil {
			t.Fatal(err)
		}
	}
}

func TestF8CLI051Journey(t *testing.T) {
	data := t.TempDir()
	seedJourney(t, data)
	code, out, errs := runF8CLI(t, runJourney, "--data-dir", data, "list", "--json")
	var rows []f8JourneyEntry
	if code != f8ExitOK || json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 2 {
		t.Fatalf("journey list = %d %q %q", code, out, errs)
	}
	if rows[0].Name != "alpha" || rows[1].Name != "beta" {
		t.Fatalf("order = %+v", rows)
	}
	if rows[1].Source != "[REDACTED:source]" {
		t.Fatalf("missing source not redacted: %+v", rows[1])
	}
	journeyMutateChecks(t, data, rows[0].Key, rows[1].Key)
	if code, _, errs = runF8CLI(t, runJourney, "--data-dir", data, "edit", rows[1].Key); code != f8ExitUsage || !strings.Contains(errs, "usage") {
		t.Fatalf("edit without note = %d %q", code, errs)
	}
	if code, _, errs = runF8CLI(t, runJourney, "--data-dir", data, "nope"); code != f8ExitUsage || !strings.Contains(errs, "unknown subcommand") {
		t.Fatalf("unknown = %d %q", code, errs)
	}
	for _, alias := range []string{"learning", "memory-graph"} {
		var out, errb bytes.Buffer
		if code, ok := dispatchCommand([]string{alias, "--data-dir", data, "list"}, &out, &errb); !ok || code != f8ExitOK {
			t.Fatalf("alias %s = %d ok=%t %q", alias, code, ok, errb.String())
		}
		if !strings.Contains(out.String(), "note=hello") {
			t.Fatalf("alias %s did not reach journey: %q", alias, out.String())
		}
	}
}

func journeyMutateChecks(t *testing.T, data, first, second string) {
	t.Helper()
	code, out, _ := runF8CLI(t, runJourney, "--data-dir", data, "delete", first)
	if code != f8ExitOK || !strings.Contains(out, "deleted") {
		t.Fatalf("delete = %d %q", code, out)
	}
	if code, out, _ = runF8CLI(t, runJourney, "--data-dir", data, "delete", first); code != f8ExitOK || out != "" {
		t.Fatalf("idempotent delete = %d %q", code, out)
	}
	if code, out, _ = runF8CLI(t, runJourney, "--data-dir", data, "edit", second, "--note", "hello"); code != f8ExitOK || !strings.Contains(out, "edited") {
		t.Fatalf("edit = %d %q", code, out)
	}
	if _, out, _ = runF8CLI(t, runJourney, "--data-dir", data, "list"); !strings.Contains(out, "note=hello") {
		t.Fatalf("note missing: %q", out)
	}
}

func TestF8CLI050Pets(t *testing.T) {
	data := t.TempDir()
	asset := filepath.Join(t.TempDir(), "pet.bin")
	if err := os.WriteFile(asset, bytes.Repeat([]byte{1}, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := runF8CLI(t, runPets, "--data-dir", data, "install", "byte", "--from", asset, "--description", "a byte"); code != f8ExitOK || !strings.Contains(out, "installed byte") {
		t.Fatalf("install = %d %q", code, out)
	}
	if code, out, _ := runF8CLI(t, runPets, "--data-dir", data, "install", "byte", "--from", asset); code != f8ExitOK || !strings.Contains(out, "already installed") {
		t.Fatalf("idempotent install = %d %q", code, out)
	}
	petsLifecycleChecks(t, data)
	petsRemoteChecks(t, data)
	big := filepath.Join(t.TempDir(), "big.bin")
	if err := os.WriteFile(big, bytes.Repeat([]byte{1}, f8PetAssetMax+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := runF8CLI(t, runPets, "--data-dir", data, "install", "big", "--from", big); code != f8ExitUsage {
		t.Fatalf("oversized asset = %d", code)
	}
	if code, _, _ := runF8CLI(t, runPets, "--data-dir", data, "install", "../x", "--from", asset); code != f8ExitUsage {
		t.Fatalf("path pet name = %d", code)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(data, "pets.json"))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("store mode = %v, %v", fi, err)
		}
	}
}

func petsLifecycleChecks(t *testing.T, data string) {
	t.Helper()
	if code, out, _ := runF8CLI(t, runPets, "--data-dir", data, "list"); code != f8ExitOK || !strings.Contains(out, "byte scale=medium") {
		t.Fatalf("list = %d %q", code, out)
	}
	if code, out, _ := runF8CLI(t, runPets, "--data-dir", data, "select", "byte"); code != f8ExitOK || !strings.Contains(out, "selected") {
		t.Fatalf("select = %d %q", code, out)
	}
	if _, out, _ := runF8CLI(t, runPets, "--data-dir", data, "show", "byte"); !strings.Contains(out, "sha256: ") {
		t.Fatalf("show = %q", out)
	}
	if code, out, _ := runF8CLI(t, runPets, "--data-dir", data, "scale", "byte", "large"); code != f8ExitOK || !strings.Contains(out, "large") {
		t.Fatalf("scale = %d %q", code, out)
	}
	if code, out, _ := runF8CLI(t, runPets, "--data-dir", data, "doctor"); code != f8ExitOK || !strings.Contains(out, "check active ok pet=byte") {
		t.Fatalf("doctor = %d %q", code, out)
	}
	if code, out, _ := runF8CLI(t, runPets, "--data-dir", data, "off"); code != f8ExitOK || !strings.Contains(out, "pet off") {
		t.Fatalf("off = %d %q", code, out)
	}
	if code, _, _ := runF8CLI(t, runPets, "--data-dir", data, "remove", "byte"); code != f8ExitOK {
		t.Fatalf("remove = %d", code)
	}
	if code, out, _ := runF8CLI(t, runPets, "--data-dir", data, "remove", "byte"); code != f8ExitOK || out != "" {
		t.Fatalf("idempotent remove = %d %q", code, out)
	}
}

// petsRemoteChecks installs from an opt-in fake petdex server and proves the
// default local path never dials (CLI-050, UI-010).
func petsRemoteChecks(t *testing.T, data string) {
	t.Helper()
	srv, hits, sum, asset := petsFakeServer(t)
	defer srv.Close()
	petsRemoteInstallCheck(t, data, srv.URL, hits, sum)
	petsLocalNoDialCheck(t, data, hits, asset)
	petsRemoteRefusalCheck(t, data, srv.URL)
}

func petsFakeServer(t *testing.T) (*httptest.Server, *atomic.Int32, [32]byte, []byte) {
	t.Helper()
	asset := bytes.Repeat([]byte{7}, 48)
	sum := sha256.Sum256(asset)
	hits := &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/manifest.json":
			if _, err := fmt.Fprintf(w, `{"pets":[{"slug":"remote","description":"a remote pet","style":"pixel","asset":"/pets/remote.bin","sha256":"%x"}]}`, sum); err != nil {
				return
			}
		case "/pets/remote.bin":
			if _, err := w.Write(asset); err != nil {
				return
			}
		default:
			http.NotFound(w, r)
		}
	}))
	return srv, hits, sum, asset
}

func petsRemoteInstallCheck(t *testing.T, data, base string, hits *atomic.Int32, sum [32]byte) {
	t.Helper()
	if code, out, errs := runF8CLI(t, runPets, "--data-dir", data, "install", "remote", "--url", base+"/manifest.json"); code != f8ExitOK || !strings.Contains(out, "installed remote sha256="+hex.EncodeToString(sum[:])) || !strings.Contains(out, "style=pixel") {
		t.Fatalf("remote install = %d %q %q", code, out, errs)
	}
	if hits.Load() != 2 {
		t.Fatalf("petdex hits = %d, want 2", hits.Load())
	}
	if code, out, _ := runF8CLI(t, runPets, "--data-dir", data, "show", "remote"); code != f8ExitOK || !strings.Contains(out, "description: a remote pet") || !strings.Contains(out, "style: pixel") {
		t.Fatalf("show remote = %d %q", code, out)
	}
}

func petsLocalNoDialCheck(t *testing.T, data string, hits *atomic.Int32, asset []byte) {
	t.Helper()
	before := hits.Load()
	if code, _, _ := runF8CLI(t, runPets, "--data-dir", data, "install", "local", "--from", writePetAsset(t, asset)); code != f8ExitOK {
		t.Fatalf("local install = %d", code)
	}
	if hits.Load() != before {
		t.Fatal("local install dialed petdex")
	}
}

func petsRemoteRefusalCheck(t *testing.T, data, base string) {
	t.Helper()
	if code, _, errs := runF8CLI(t, runPets, "--data-dir", data, "install", "bad", "--url", "http://petdex.invalid/x"); code != f8ExitUsage || !strings.Contains(errs, "loopback") {
		t.Fatalf("remote http = %d %q", code, errs)
	}
	if code, _, _ := runF8CLI(t, runPets, "--data-dir", data, "install", "ghost", "--url", base+"/manifest.json"); code != f8ExitUsage {
		t.Fatalf("unknown remote pet = %d", code)
	}
}

func writePetAsset(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pet.bin")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestF8CLI049Curator(t *testing.T) {
	data := t.TempDir()
	old := time.Now().Add(-31 * 24 * time.Hour).Unix()
	for _, name := range []string{"alpha", "beta"} {
		if err := skills.AppendLedger(data, &skills.LedgerEntry{At: old, Name: name, SHA256: name, Source: "/s", Action: "indexed"}); err != nil {
			t.Fatal(err)
		}
	}
	if code, out, _ := runF8CLI(t, runCurator, "--data-dir", data, "status"); code != f8ExitOK || !strings.Contains(out, "paused=false") {
		t.Fatalf("status = %d %q", code, out)
	}
	if code, out, _ := runF8CLI(t, runCurator, "--data-dir", data, "usage"); code != f8ExitOK || !strings.Contains(out, "alpha indexed=1") {
		t.Fatalf("usage = %d %q", code, out)
	}
	if code, out, _ := runF8CLI(t, runCurator, "--data-dir", data, "run"); code != f8ExitOK || !strings.Contains(out, "proposal archive alpha") {
		t.Fatalf("run = %d %q", code, out)
	}
	if _, out, _ := runF8CLI(t, runCurator, "--data-dir", data, "ledger"); !strings.Contains(out, "proposal:archive") {
		t.Fatalf("ledger = %q", out)
	}
	curatorMutations(t, data)
	curatorBackupChecks(t, data)
	if code, _, _ := runF8CLI(t, runCurator, "--data-dir", data, "purge"); code != f8ExitUsage {
		t.Fatalf("purge without yes = %d", code)
	}
	if code, out, _ := runF8CLI(t, runCurator, "--data-dir", data, "purge", "--yes"); code != f8ExitOK || !strings.Contains(out, "purged") {
		t.Fatalf("purge = %d %q", code, out)
	}
	if code, out, _ := runF8CLI(t, runCurator, "--data-dir", data, "purge", "--yes"); code != f8ExitOK || out != "" {
		t.Fatalf("idempotent purge = %d %q", code, out)
	}
}

func curatorMutations(t *testing.T, data string) {
	t.Helper()
	for range 2 {
		if code, _, _ := runF8CLI(t, runCurator, "--data-dir", data, "pin", "alpha"); code != f8ExitOK {
			t.Fatalf("pin = %d", code)
		}
	}
	if _, out, _ := runF8CLI(t, runCurator, "--data-dir", data, "status"); !strings.Contains(out, "pins=1") {
		t.Fatalf("pin not idempotent: %q", out)
	}
	if _, out, _ := runF8CLI(t, runCurator, "--data-dir", data, "list-unmanaged"); !strings.Contains(out, "alpha") {
		t.Fatalf("list-unmanaged = %q", out)
	}
	_, _, _ = runF8CLI(t, runCurator, "--data-dir", data, "adopt", "alpha")
	if _, out, _ := runF8CLI(t, runCurator, "--data-dir", data, "list-unmanaged"); strings.Contains(out, "alpha") {
		t.Fatalf("adopt did not remove alpha: %q", out)
	}
	_, _, _ = runF8CLI(t, runCurator, "--data-dir", data, "archive", "alpha")
	if _, out, _ := runF8CLI(t, runCurator, "--data-dir", data, "list-archived"); !strings.Contains(out, "alpha") {
		t.Fatalf("list-archived = %q", out)
	}
	_, _, _ = runF8CLI(t, runCurator, "--data-dir", data, "restore", "alpha")
	if _, out, _ := runF8CLI(t, runCurator, "--data-dir", data, "list-archived"); strings.Contains(out, "alpha") {
		t.Fatalf("restore left archived: %q", out)
	}
	if _, out, _ := runF8CLI(t, runCurator, "--data-dir", data, "prune"); !strings.Contains(out, "pruned") && out != "" {
		t.Fatalf("prune = %q", out)
	}
	if code, _, _ := runF8CLI(t, runCurator, "--data-dir", data, "pause"); code != f8ExitOK {
		t.Fatalf("pause = %d", code)
	}
	if code, _, errs := runF8CLI(t, runCurator, "--data-dir", data, "run"); code != f8ExitFail || !strings.Contains(errs, "paused") {
		t.Fatalf("run paused = %d %q", code, errs)
	}
	if code, _, _ := runF8CLI(t, runCurator, "--data-dir", data, "resume"); code != f8ExitOK {
		t.Fatalf("resume = %d", code)
	}
}

func curatorBackupChecks(t *testing.T, data string) {
	t.Helper()
	backup := filepath.Join(data, "backup.json")
	if code, out, _ := runF8CLI(t, runCurator, "--data-dir", data, "backup", "--out", backup); code != f8ExitOK || !strings.Contains(out, "backup written") {
		t.Fatalf("backup = %d %q", code, out)
	}
	_, _, _ = runF8CLI(t, runCurator, "--data-dir", data, "pin", "beta")
	if code, out, _ := runF8CLI(t, runCurator, "--data-dir", data, "rollback", "--from", backup); code != f8ExitOK || !strings.Contains(out, "rolled back") {
		t.Fatalf("rollback = %d %q", code, out)
	}
	_, out, _ := runF8CLI(t, runCurator, "--data-dir", data, "status")
	if strings.Contains(out, "pins=2") {
		t.Fatalf("rollback kept later pin: %q", out)
	}
}
