package exec

import (
	"errors"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/redact"
)

// TestNormalizeSpecLimits covers the defaulting and clamping matrix of
// Spec.normalize: the caller may leave limits zero, but a value outside
// the documented bounds is rejected or clamped, never silently honored.
func TestNormalizeSpecLimits(t *testing.T) {
	t.Parallel()
	for _, argv := range [][]string{nil, {}, {""}} {
		s := Spec{Argv: argv}
		if err := s.normalize(); !errors.Is(err, ErrSpec) {
			t.Errorf("argv %q: err %v, want ErrSpec", argv, err)
		}
	}
	s := Spec{Argv: []string{"true"}}
	if err := s.normalize(); err != nil {
		t.Fatal(err)
	}
	if s.Timeout != DefaultTimeout || s.IdleTimeout != DefaultIdleTimeout || s.KillGrace != DefaultKillGrace {
		t.Errorf("defaults = %v/%v/%v", s.Timeout, s.IdleTimeout, s.KillGrace)
	}
	s = Spec{Argv: []string{"true"}, Timeout: MaxTimeout}
	if err := s.normalize(); err != nil {
		t.Errorf("timeout at max rejected: %v", err)
	}
	s.Timeout = MaxTimeout + time.Nanosecond
	if err := s.normalize(); !errors.Is(err, ErrSpec) {
		t.Errorf("timeout above max: %v", err)
	}
	s = Spec{Argv: []string{"true"}, IdleTimeout: -time.Second}
	if err := s.normalize(); err != nil || s.IdleTimeout != -time.Second {
		t.Errorf("disabled idle timeout = %v, err %v", s.IdleTimeout, err)
	}
	for _, c := range []struct{ in, want time.Duration }{
		{time.Millisecond, MinKillGrace},
		{-time.Second, MinKillGrace},
		{time.Hour, MaxKillGrace},
		{2 * time.Second, 2 * time.Second},
	} {
		s := Spec{Argv: []string{"true"}, KillGrace: c.in}
		if err := s.normalize(); err != nil || s.KillGrace != c.want {
			t.Errorf("KillGrace %v: got %v, err %v", c.in, s.KillGrace, err)
		}
	}
}

// TestMarkerEnvInjection: an inherited marker must be replaced, never
// duplicated, so the sweep of one call cannot reap another call's tree.
func TestMarkerEnvInjection(t *testing.T) {
	t.Parallel()
	got := withMarker([]string{"PATH=/bin", "DAMNBOT=0", "DAMNBOT_EXEC_ID=stale", "HOME=/h"}, "abc")
	want := []string{"PATH=/bin", "HOME=/h", "DAMNBOT_EXEC_ID=abc", "DAMNBOT=1"}
	if !slices.Equal(got, want) {
		t.Errorf("withMarker = %q, want %q", got, want)
	}
	again := withMarker(got, "xyz")
	wantAgain := []string{"PATH=/bin", "HOME=/h", "DAMNBOT_EXEC_ID=xyz", "DAMNBOT=1"}
	if !slices.Equal(again, wantAgain) {
		t.Errorf("second marker pass = %q, want %q", again, wantAgain)
	}
}

// TestExecIDShape: the "<owner pid>.<random>" form lets the crash sweep
// tell a dead damnbot's trees from a live one's.
func TestExecIDShape(t *testing.T) {
	t.Parallel()
	id := newExecID()
	prefix := strconv.Itoa(os.Getpid()) + "."
	if !strings.HasPrefix(id, prefix) {
		t.Fatalf("id %q lacks owner pid prefix %q", id, prefix)
	}
	random := strings.TrimPrefix(id, prefix)
	if len(random) != 16 {
		t.Fatalf("random part %q has %d chars, want 16", random, len(random))
	}
	for _, c := range random {
		if !strings.ContainsRune("0123456789abcdef", c) {
			t.Fatalf("random part %q is not lowercase hex", random)
		}
	}
	if other := newExecID(); other == id {
		t.Fatalf("two calls returned the same id %q", id)
	}
}

// TestSanitizePromptPrintable: a detected prompt is target output that goes
// to the model and UI (EX45-1), so only printable runes survive and known
// secrets are redacted.
func TestSanitizePromptPrintable(t *testing.T) {
	t.Parallel()
	red := redact.New(redact.Options{Secrets: []string{"hunter2"}})
	for _, c := range []struct{ in, want string }{
		{"continue? ", "continue? "},
		{"a\x00b\x07c", "abc"},
		{"a\u0085b\u009fc", "abc"},
		{"line\ttab\nnext", "line\ttab\nnext"},
		{"pw=hunter2>", "pw=[REDACTED:secret]>"},
		{"émoji 🙂", "émoji 🙂"},
	} {
		if got := sanitizePrompt(red, c.in); got != c.want {
			t.Errorf("sanitizePrompt(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := sanitizePrompt(nil, "a\x00b"); got != "ab" {
		t.Errorf("nil redactor = %q, want ab", got)
	}
	if got := sanitizePrompt(red, ""); got != "" {
		t.Errorf("empty = %q", got)
	}
}

// TestEscapeLenBounds: an unterminated CSI/OSC/DCS may not hide the rest of
// the output; escapeLen consumes at most maxEscape bytes (EX47-1).
func TestEscapeLenBounds(t *testing.T) {
	t.Parallel()
	long := "\x1b[" + strings.Repeat("1", maxEscape*2)
	for _, c := range []struct {
		in   string
		want int
	}{
		{"\x1b[31m", 5},
		{"\x1b[31", 4},
		{"\x1b]0;t\x07", 6},
		{"\x1b]0;t\x1b\\", 7},
		{"\x1bPabc", 5},
		{"\x1bX", 2},
		{long, maxEscape},
	} {
		if got := escapeLen([]byte(c.in)); got != c.want {
			t.Errorf("escapeLen(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestApplyCRTerminalSemantics(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ in, want string }{
		{"10%\r50%", "50%"},
		{"done\r", "done"},
		{"a\rb\r", "b"},
		{"abc\b\bX", "aX"},
		{"abc\b\b\b\b", ""},
		{"a\tb", "a\tb"},
		{"a\x00b\x7fc", "abc"},
		{"a\u0085b", "ab"},
		{"a\xffb", "a\uFFFDb"},
		{"plain", "plain"},
	} {
		if got := applyCR([]byte(c.in)); got != c.want {
			t.Errorf("applyCR(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestHasCtlDetection(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		in   string
		want bool
	}{
		{"plain text", false},
		{"a\tb", false},
		{"a\x1fb", true},
		{"a\x7fb", true},
		{"a\xc2\x85b", true},
		{"a\xc2\xa0b", false},
		{"a\xffb", false},
	} {
		if got := hasCtl([]byte(c.in)); got != c.want {
			t.Errorf("hasCtl(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestGroupThousandsFormat(t *testing.T) {
	t.Parallel()
	for in, want := range map[int64]string{
		0: "0", 7: "7", 999: "999", 1000: "1.000", 2341: "2.341",
		15815: "15.815", 1234567: "1.234.567", -1234: "-1.234",
	} {
		if got := groupThousands(in); got != want {
			t.Errorf("groupThousands(%d) = %q, want %q", in, got, want)
		}
	}
}

// TestSecretNoticeWording: the notice names every referenced secret and
// states that it is not injected; an unrelated reference stays silent.
func TestSecretNoticeWording(t *testing.T) {
	t.Parallel()
	secrets := []SecretEnv{{Name: "TOKEN", Value: "v1"}, {Name: "KEY", Value: "v2"}}
	notice := secretNotice(`curl -H "Authorization: $TOKEN" "${KEY}"`, secrets)
	for _, want := range []string{"TOKEN", "KEY", "not injected"} {
		if !strings.Contains(notice, want) {
			t.Errorf("notice %q missing %q", notice, want)
		}
	}
	if got := secretNotice("true", secrets); got != "" {
		t.Errorf("notice for a literal command = %q", got)
	}
	if got := secretNotice("echo $OTHER", secrets); got != "" {
		t.Errorf("notice for an unrelated ref = %q", got)
	}
}

// TestCandidatesPerOS: PATH lookup tries only real executables; on Windows
// CreateProcess would otherwise start cmd.exe for .bat/.cmd, so only .exe
// and .com candidates are produced.
func TestCandidatesPerOS(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "windows" {
		if got := candidates("/bin/tool"); !slices.Equal(got, []string{"/bin/tool"}) {
			t.Errorf("candidates = %q", got)
		}
		return
	}
	for _, c := range []struct {
		in   string
		want []string
	}{
		{`C:\bin\tool`, []string{`C:\bin\tool.exe`, `C:\bin\tool.com`}},
		{`C:\bin\tool.EXE`, []string{`C:\bin\tool.EXE`}},
	} {
		if got := candidates(c.in); !slices.Equal(got, c.want) {
			t.Errorf("candidates(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestEnvPathSelection(t *testing.T) {
	t.Parallel()
	if got := envPath([]string{"HOME=/h", "PATH=/a:/b"}); got != "/a:/b" {
		t.Errorf("envPath = %q", got)
	}
	if got := envPath([]string{"HOME=/h"}); got != "" {
		t.Errorf("envPath without PATH = %q", got)
	}
}
