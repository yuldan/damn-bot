package exec

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func newSessions(t testing.TB, env []string) *Sessions {
	t.Helper()
	r := New(context.Background())
	if env == nil {
		env = (&EnvPolicy{}).Env(append(os.Environ(), "HOME="+t.TempDir()), true, "", "", "")
	}
	ss := r.NewSessions(t.TempDir(), env)
	t.Cleanup(func() {
		if err := errors.Join(ss.Close(), r.Close()); err != nil {
			t.Error(err)
		}
	})
	return ss
}

func sessExec(t testing.TB, ss *Sessions, name, cmd string, timeout time.Duration) Result {
	t.Helper()
	res, err := ss.Exec(t.Context(), name, cmd, nil, timeout)
	if err != nil {
		t.Fatalf("%q: %v", cmd, err)
	}
	return res
}

// TestShellSessionLimits (EXC-08): at most 4 sessions; idle ones are
// reaped after 30 minutes.
func TestShellSessionLimits(t *testing.T) {
	t.Parallel()
	ss := newSessions(t, nil)
	now := time.Now()
	ss.now = func() time.Time { return now }
	for _, n := range []string{"a", "b", "c", "d"} {
		sessExec(t, ss, n, "echo ok", 10*time.Second)
	}
	if _, err := ss.Exec(t.Context(), "e", "echo ok", nil, time.Second); !errors.Is(err, ErrSessionLimit) {
		t.Fatalf("5th session: %v", err)
	}
	for _, bad := range []string{"", "UPPER", "a b", strings.Repeat("x", 33)} {
		if _, err := ss.Exec(t.Context(), bad, "echo ok", nil, time.Second); !errors.Is(err, ErrSessionName) {
			t.Fatalf("name %q: %v", bad, err)
		}
	}
	now = now.Add(SessionIdleReap)
	if res := sessExec(t, ss, "e", "echo ok", 10*time.Second); res.Status != StatusExited {
		t.Fatalf("after idle reap: %+v", res)
	}
}

// TestStripFrames (EX63-1): a well-formed protocol frame is removed from
// command output; plain 0x1E text and malformed frames are preserved.
func TestStripFrames(t *testing.T) {
	t.Parallel()
	nonce := strings.Repeat("ab", 16)
	frame := string(sentinelMark) + "DB:" + nonce + ":0:/tmp" + string(sentinelMark)
	if got := string(stripFrames([]byte("before" + frame + "after"))); got != "beforeafter" {
		t.Fatalf("%q", got)
	}
	if in := "plain " + string(sentinelMark) + "still text"; string(stripFrames([]byte(in))) != in {
		t.Fatalf("unterminated mark: %q", stripFrames([]byte(in)))
	}
	if in := "a" + string(sentinelMark) + "DB:not-a-frame" + string(sentinelMark) + "b"; string(stripFrames([]byte(in))) != in {
		t.Fatalf("non-frame: %q", stripFrames([]byte(in)))
	}
}
