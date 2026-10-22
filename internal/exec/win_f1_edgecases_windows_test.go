//go:build windows

package exec

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// TestWindowsF1EdgeCases covers the EC-WIN entries the roadmap places at F1
// that exec owns: a non-UTF-8 console must not crash the child (EC-WIN-001)
// and children are forced to UTF-8 while the parent decodes it explicitly
// (EC-WIN-016).
func TestWindowsF1EdgeCases(t *testing.T) {
	t.Parallel()
	g := phase.Current(t)
	cases := []struct {
		id string
		fn func(*testing.T)
	}{
		{"EC-WIN-001", ecWin001},
		{"EC-WIN-016", ecWin016},
	}
	for _, c := range cases {
		if g.Item(t, c.id, "F1", "covered") {
			t.Run(c.id, c.fn)
		}
	}
}

const consoleHelperEnv = "DAMNBOT_TEST_CONSOLE_HELPER"

// TestConsoleHelper writes UTF-8 text to a code page 1252 console. It only
// acts when the parent sets consoleHelperEnv, so a normal run of the package
// just returns.
func TestConsoleHelper(t *testing.T) {
	if os.Getenv(consoleHelperEnv) == "" {
		return
	}
	if err := allocConsole(); err != nil && !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("AllocConsole: %v", err)
	}
	const cp1252 = 1252
	if err := windows.SetConsoleOutputCP(cp1252); err != nil {
		t.Fatalf("SetConsoleOutputCP: %v", err)
	}
	f, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("CONOUT$: %v", err)
	}
	if _, err := f.WriteString("héllo 🙂\n"); err != nil {
		t.Fatalf("console write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// EC-WIN-001: unicode output to a cp1252 console must not crash the process
// (the console substitutes glyphs; the write itself cannot fail with an
// encoding error).
func ecWin001(t *testing.T) {
	c := exec.Command(os.Args[0], "-test.run=^TestConsoleHelper$") //nolint:gosec // G204: re-exec of the test binary
	c.Env = append(os.Environ(), consoleHelperEnv+"=1")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("console child failed: %v\n%s", err, out)
	}
}

// EC-WIN-016: every child gets an explicit UTF-8 stdio contract, so a
// Python tool cannot pick the Windows ANSI code page; the parent side always
// decodes UTF-8 (exec.applyCR maps invalid bytes to U+FFFD).
func ecWin016(t *testing.T) {
	var env EnvPolicy
	got := map[string]string{}
	for _, kv := range env.Env([]string{"PATH=" + os.Getenv("PATH")}, false, "", "", "") {
		k, v, ok := strings.Cut(kv, "=")
		if ok {
			got[strings.ToUpper(k)] = v
		}
	}
	for name, want := range map[string]string{"PYTHONUTF8": "1", "PYTHONIOENCODING": "utf-8"} {
		if got[name] != want {
			t.Fatalf("%s = %q, want %q", name, got[name], want)
		}
	}
	// A user value still wins over the non-interactive default.
	env.Set = map[string]string{"PYTHONIOENCODING": "latin-1"}
	override := env.Env(nil, false, "", "", "")
	if !hasEnv(override, "PYTHONIOENCODING=latin-1") {
		t.Fatal("exec.env could not override the default")
	}
}

func hasEnv(env []string, kv string) bool {
	for _, e := range env {
		if e == kv {
			return true
		}
	}
	return false
}

// allocConsole attaches a console to the calling process (x/sys has no
// wrapper for AllocConsole); a failed call is the errno of the API.
func allocConsole() error {
	st, _, err := windows.NewLazySystemDLL("kernel32.dll").NewProc("AllocConsole").Call()
	if st == 0 {
		return err
	}
	return nil
}
