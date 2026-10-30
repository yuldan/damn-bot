//go:build windows

package main

import (
	"testing"

	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// TestServiceWindowsEdgeCases catalogs the F4 Windows rows on the Windows CI
// runner (ADR-080): status never claims a supervisor without evidence
// (EC-WIN-003), install and uninstall retire the old launcher's task names
// (EC-WIN-004) and schtasks output is decoded strictly with a UTF-8 fallback,
// never panicking (EC-WIN-006). It lives in a Windows-only file because the
// real schtasks round-trip only compiles there; the catalog scan reads the
// file text on every platform.
func TestServiceWindowsEdgeCases(t *testing.T) {
	g := phase.Current(t)
	if g.Item(t, "EC-WIN-003", "F4", "covered") {
		t.Run("EC-WIN-003", func(t *testing.T) {
			t.Run("real probe", TestWindowsSupervisionProbe)
			t.Run("status matrix", TestWinStatusSupervision)
		})
	}
	if g.Item(t, "EC-WIN-004", "F4", "covered") {
		t.Run("EC-WIN-004", func(t *testing.T) {
			t.Run("names", TestWinTaskNameIsProfileScoped)
			t.Run("install retires legacy", TestWinInstallEnableAndRetiresLegacy)
			t.Run("uninstall retires all", TestWinUninstallRetiresAllNamesKeepsData)
		})
	}
	if g.Item(t, "EC-WIN-006", "F4", "covered") {
		t.Run("EC-WIN-006", func(t *testing.T) {
			t.Run("utf-16", TestDecodeTaskOutputUTF16LE)
			t.Run("utf-8 fallback", TestDecodeTaskOutputUTF8Fallback)
			t.Run("broken", TestDecodeTaskOutputRejectsBroken)
			t.Run("xml round-trip", TestRenderTaskXMLUTF16RoundTrip)
		})
	}
}
