package obs

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func testLogger(t *testing.T, o LogOptions) (*bytes.Buffer, *slog.Logger) {
	t.Helper()
	var buf bytes.Buffer
	if o.Salt == nil {
		o.Salt = []byte("0123456789abcdef0123456789abcdef")
	}
	h, err := NewLogHandler(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}), o)
	if err != nil {
		t.Fatal(err)
	}
	return &buf, slog.New(h)
}

func records(t *testing.T, raw string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(raw), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("record %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

// TestLogContentFreeAtInfo is the GAP-SEC-11 default: every deny-list key is
// replaced by length+hash at INFO, and the value never reaches the writer.
func TestLogContentFreeAtInfo(t *testing.T) {
	const canary = "PROMPT-CANARY-INFo-91"
	buf, log := testLogger(t, LogOptions{})
	log.Info("turn", "text", canary, "content", canary, "message", canary, "reply_to_text", canary,
		"caption", canary, "username", canary, "first_name", canary, "last_name", canary,
		"chat_title", canary, "args", canary, "query", canary, "prompt", canary)
	if strings.Contains(buf.String(), canary) {
		t.Fatalf("content reached INFO log: %s", buf.String())
	}
	m := records(t, buf.String())[0]
	for _, key := range []string{
		"args", "caption", "chat_title", "content", "first_name", "last_name",
		"message", "prompt", "query", "reply_to_text", "text", "username",
	} {
		if _, ok := m[key]; ok {
			t.Errorf("deny-list key %q survived: %v", key, m)
		}
		if _, ok := m[key+"_len"]; !ok {
			t.Errorf("missing %s_len: %v", key, m)
		}
	}
}

// TestLogPlatformIDsHMAC: chat_id/user_id/sender are keyed and stable, and
// the raw identifier never appears.
func TestLogPlatformIDsHMAC(t *testing.T) {
	buf, log := testLogger(t, LogOptions{})
	log.Info("inbound", "chat_id", "123456789", "user_id", "777", "sender", "777")
	out := buf.String()
	for _, raw := range []string{"123456789", `"777"`} {
		if strings.Contains(out, raw) {
			t.Fatalf("raw id %s in log: %s", raw, out)
		}
	}
	m := records(t, out)[0]
	chat := mapStr(t, m, "chat_id")
	user := mapStr(t, m, "user_id")
	sender := mapStr(t, m, "sender")
	if len(chat) != 12 || len(user) != 12 {
		t.Fatalf("hmac lengths: chat=%q user=%q", chat, user)
	}
	if user != sender {
		t.Fatalf("same id hashed differently: %q vs %q", user, sender)
	}
	if chat == user {
		t.Fatal("different ids produced the same hmac")
	}
}

// TestLogDebugNeedsFlag: DEBUG metadata is content-free unless the flag is on.
func TestLogDebugNeedsFlag(t *testing.T) {
	const canary = "DEBUG-CANARY-off"
	buf, log := testLogger(t, LogOptions{})
	log.Debug("request", "text", canary)
	if strings.Contains(buf.String(), canary) {
		t.Fatalf("content at DEBUG without flag: %s", buf.String())
	}
}

// TestLogContentTTL: content is kept only inside the enable window and the
// handler warns exactly once when the window closes.
func TestLogContentTTL(t *testing.T) {
	const canary = "TTL-CANARY-window"
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	buf, log := testLogger(t, LogOptions{
		MessageContent: true, TTL: time.Hour, Now: func() time.Time { return now },
	})
	log.Debug("request", "text", canary)
	if !strings.Contains(buf.String(), canary) {
		t.Fatalf("content missing inside ttl: %s", buf.String())
	}
	now = now.Add(2 * time.Hour)
	log.Debug("request", "text", canary)
	out := buf.String()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if strings.Contains(lines[len(lines)-1], canary) {
		t.Fatalf("content survived ttl: %s", out)
	}
	if n := strings.Count(out, "log.message_content disabled after ttl"); n != 1 {
		t.Fatalf("ttl warning count = %d:\n%s", n, out)
	}
}

// TestLogValueNeverWritten is the negative control of the scanner used by the
// E2E canary test: a raw (unwrapped) JSON line is visible, the same record
// through LogHandler is not. If the scanner cannot see the raw line the E2E
// test would be vacuous.
func TestLogValueNeverWritten(t *testing.T) {
	const canary = "RAW-CANARY-visible"
	if !strings.Contains(`{"level":"INFO","text":"RAW-CANARY-visible"}`, canary) {
		t.Fatal("scanner cannot see a raw canary; the E2E control is vacuous")
	}
	buf, log := testLogger(t, LogOptions{})
	log.Info("raw", "text", canary)
	if strings.Contains(buf.String(), canary) {
		t.Fatalf("handler leaked canary: %s", buf.String())
	}
}

// TestParseLevel: config level names map to slog levels.
func TestParseLevel(t *testing.T) {
	for name, want := range map[string]slog.Level{
		"": slog.LevelInfo, "info": slog.LevelInfo, "DEBUG": slog.LevelDebug,
		"warn": slog.LevelWarn, "error": slog.LevelError,
	} {
		got, err := ParseLevel(name)
		if err != nil || got != want {
			t.Errorf("ParseLevel(%q) = %v, %v", name, got, err)
		}
	}
	if _, err := ParseLevel("trace"); err == nil {
		t.Fatal("trace accepted")
	}
}

// mapStr reads a string field or fails the test.
func mapStr(t *testing.T, m map[string]any, key string) string {
	t.Helper()
	v, ok := m[key].(string)
	if !ok {
		t.Fatalf("%s = %v, want string (%v)", key, m[key], m)
	}
	return v
}
