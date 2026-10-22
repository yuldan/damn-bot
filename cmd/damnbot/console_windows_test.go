//go:build windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/damnbot/damnbot/internal/testutil/phase"
)

const cpHelperEnv = "DAMNBOT_TEST_CP_HELPER"

// TestUTF8ConsoleHelper checks the code page after utf8Console in a child
// that owns a console. A normal package run just returns.
func TestUTF8ConsoleHelper(t *testing.T) {
	if os.Getenv(cpHelperEnv) == "" {
		return
	}
	if err := allocConsole(); err != nil && !isAccessDenied(err) {
		t.Fatalf("AllocConsole: %v", err)
	}
	utf8Console()
	if cp, err := windows.GetConsoleOutputCP(); err != nil || cp != cpUTF8 {
		t.Fatalf("output code page = %d (%v), want %d", cp, err, cpUTF8)
	}
	if cp, err := windows.GetConsoleCP(); err != nil || cp != cpUTF8 {
		t.Fatalf("input code page = %d (%v), want %d", cp, err, cpUTF8)
	}
}

// EC-WIN-017: the Windows launcher sets the console code page to UTF-8, so
// the shells it starts do not encode unicode with the OEM/ANSI page.
func TestWindowsF1EdgeCases(t *testing.T) {
	g := phase.Current(t)
	if g.Item(t, "EC-WIN-017", "F1", "covered") {
		t.Run("EC-WIN-017", ecWin017)
	}
}

func ecWin017(t *testing.T) {
	c := exec.Command(os.Args[0], "-test.run=^TestUTF8ConsoleHelper$") //nolint:gosec // G204: re-exec of the test binary
	c.Env = append(os.Environ(), cpHelperEnv+"=1")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("console helper failed: %v\n%s", err, out)
	}
}

func isAccessDenied(err error) bool { return errors.Is(err, windows.ERROR_ACCESS_DENIED) }

// allocConsole attaches a console to the calling process; x/sys has no
// wrapper for AllocConsole.
func allocConsole() error {
	st, _, err := windows.NewLazySystemDLL("kernel32.dll").NewProc("AllocConsole").Call()
	if st == 0 {
		return err
	}
	return nil
}
