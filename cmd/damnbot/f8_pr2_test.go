//go:build !minimal

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
	if code, _, errs := runF8CLI(t, runPets, "--data-dir", data, "install", "remote", "--url", "https://petdex.invalid/x"); code != f8ExitUnavailable || !strings.Contains(errs, "module_unavailable") {
		t.Fatalf("remote install = %d %q", code, errs)
	}
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
