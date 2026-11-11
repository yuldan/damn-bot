//go:build !minimal

package main

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/i18n"
	"github.com/damnbot/damnbot/internal/plugins/builtin"
	"github.com/damnbot/damnbot/internal/toolset"
)

// TestF8UIMatrix adalah agregat §1 "UI 28/28": dua puluh delapan baris UI
// (delapan di antaranya berfase F8), setiap request loop × lima frontend
// dirender atau punya fallback bernama, dan delapan surface F8 benar-benar
// terdaftar di registry runtime.
func TestF8UIMatrix(t *testing.T) {
	t.Parallel()
	checkUIParity(t)
	checkSurfaceMatrix(t)
	checkUISurfaces(t)
}

// checkUIParity pins 28 UI parity rows (8 F8) and the 8 closed ledger rows.
func checkUIParity(t *testing.T) {
	t.Helper()
	parity := docRows(t, "../../docs/FEATURE-PARITY.md", "UI-")
	if len(parity) != 28 {
		t.Fatalf("parity UI rows = %d, want 28", len(parity))
	}
	seen := map[string]bool{}
	f8 := 0
	for _, cells := range parity {
		if len(cells) < 9 {
			t.Errorf("%s: %d cells, want 9", cells[0], len(cells))
			continue
		}
		if seen[cells[0]] {
			t.Errorf("duplicate parity row %s", cells[0])
		}
		seen[cells[0]] = true
		if cells[7] == "F8" {
			f8++
		}
	}
	if f8 != 8 {
		t.Fatalf("parity UI rows with fase F8 = %d, want 8", f8)
	}

	ledger := docRows(t, "../../docs/F8-ACCEPTANCE.md", "UI-")
	if len(ledger) != 8 {
		t.Fatalf("ledger UI rows = %d, want 8", len(ledger))
	}
	for _, cells := range ledger {
		if len(cells) < 6 {
			t.Errorf("%s: %d cells, want 6", cells[0], len(cells))
			continue
		}
		if cells[4] != "LULUS" {
			t.Errorf("%s: status %q, want LULUS", cells[0], cells[4])
		}
	}
}

// checkSurfaceMatrix proves every loop request × frontend cell renders or has
// a named fallback.
func checkSurfaceMatrix(t *testing.T) {
	t.Helper()
	if err := checkSurfaceParity(); err != nil {
		t.Fatalf("surface contract: %v", err)
	}
	if got, want := len(surfaceContract()), len(serverRequests())*len(frontends()); got != want {
		t.Fatalf("surface rows = %d, want %d", got, want)
	}
	for _, row := range surfaceContract() {
		switch {
		case row.Mode == SurfaceRender && row.Fallback != "":
			t.Errorf("%s/%s renders with fallback %q", row.Request, row.Frontend, row.Fallback)
		case row.Mode == SurfaceFallback && strings.TrimSpace(row.Fallback) == "":
			t.Errorf("%s/%s has an empty fallback", row.Request, row.Frontend)
		case row.Mode != SurfaceRender && row.Mode != SurfaceFallback:
			t.Errorf("%s/%s has unknown mode %q", row.Request, row.Frontend, row.Mode)
		}
	}
}

// checkUISurfaces calls the runtime registries the eight F8 UI rows rely on.
func checkUISurfaces(t *testing.T) {
	t.Helper()
	checkUISurfaceCommands(t)
	checkUIDashboardAuth(t)
	checkUIDesktopToolset(t)
	checkUILocales(t)
}

func checkUISurfaceCommands(t *testing.T) {
	t.Helper()
	for _, name := range []string{"dashboard", "desktop", "serve", "pets", "proxy"} {
		entry, ok := lookupSurface(name)
		if !ok || entry.Run == nil {
			t.Errorf("surface %q is not runnable", name)
		}
	}
}

func checkUIDashboardAuth(t *testing.T) {
	t.Helper()
	dash, ok := builtin.Lookup(builtin.DashboardAuthID)
	if !ok || len(dash.Providers) != 4 || !dash.DefaultOff {
		t.Errorf("dashboard_auth = %+v (ok=%v), want four default-off providers", dash, ok)
	}
}

func checkUIDesktopToolset(t *testing.T) {
	t.Helper()
	var desktop *toolset.Definition
	builtins := toolset.Builtin()
	for i := range builtins {
		if builtins[i].Name == "desktop_ui" {
			desktop = &builtins[i]
		}
	}
	if desktop == nil || len(desktop.Tools) != 11 {
		t.Errorf("toolset desktop_ui = %+v, want 11 tools", desktop)
	}
}

func checkUILocales(t *testing.T) {
	t.Helper()
	if got := i18n.Locales(); len(got) != 18 || !slices.Contains(got, "id") {
		t.Errorf("locales = %v, want 18 including id", got)
	}
	if err := i18n.Validate(); err != nil {
		t.Errorf("i18n catalog: %v", err)
	}
	if got := f8Personas(); len(got) == 0 {
		t.Error("personality presets are empty")
	}
	if got := config.Default().Web.Listen; got != config.DefaultWebListen {
		t.Errorf("default web listen = %q, want %q", got, config.DefaultWebListen)
	}
}

// docRows returns the cells of every table row whose first cell starts with
// prefix. It is the minimal reader the aggregate needs; the ledger gate owns
// the full status parsing in internal/archtest.
func docRows(t *testing.T, path, prefix string) [][]string {
	t.Helper()
	raw, err := os.ReadFile(path) // #nosec G304 -- repository file
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var rows [][]string
	for line := range strings.Lines(string(raw)) {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "| "+prefix) {
			continue
		}
		cells := strings.Split(line, "|")
		if len(cells) < 3 {
			continue
		}
		row := make([]string, 0, len(cells)-2)
		for _, cell := range cells[1 : len(cells)-1] {
			row = append(row, strings.TrimSpace(cell))
		}
		rows = append(rows, row)
	}
	return rows
}
