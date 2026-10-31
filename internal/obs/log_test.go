package obs

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
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

// TestLogHandlerEdgeValues: non-string content values, numeric/empty ids,
// groups and With attrs are all handled without writing the value.
func TestLogHandlerEdgeValues(t *testing.T) {
	const canary = "EDGE-CANARY-55"
	buf, log := testLogger(t, LogOptions{})
	log.With("text", canary).Info("with attrs", "chat_id", "", "user_id", 42, "sender", []byte("raw-id"),
		"content", []byte(canary), "args", 12345, "prompt", true,
		slog.Group("nested", "text", canary, "username", canary))
	out := buf.String()
	for _, leak := range []string{canary, `"42"`, "raw-id", "12345", "true"} {
		if strings.Contains(out, leak) {
			t.Fatalf("edge value leaked %q: %s", leak, out)
		}
	}
	m := records(t, out)[0]
	if _, ok := m["text"]; ok {
		t.Fatalf("With text survived: %v", m)
	}
	if id := mapStr(t, m, "chat_id"); len(id) != 12 {
		t.Fatalf("empty chat_id hmac = %q", id)
	}
	if id := mapStr(t, m, "user_id"); len(id) != 12 {
		t.Fatalf("numeric user_id hmac = %q", id)
	}
	nested, ok := m["nested"].(map[string]any)
	if !ok {
		t.Fatalf("nested group missing: %v", m)
	}
	if _, ok := nested["text"]; ok {
		t.Fatalf("nested text survived: %v", nested)
	}
}

// TestSetupLogWritesContentFreeFile: the real file path (0600, dir 0700) is
// content-free at INFO and only carries body at DEBUG with the flag on; the
// per-install salt is stable and 32 bytes.
func TestSetupLogWritesContentFreeFile(t *testing.T) {
	const canary = "FILE-CANARY-77"
	dir := t.TempDir()
	stop, err := SetupLog(dir, "serve", "debug", true, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	slog.Info("info", "text", canary, "chat_id", "123")
	slog.Debug("debug", "text", canary)
	if cerr := stop(); cerr != nil {
		t.Fatal(cerr)
	}
	assertLogContent(t, dir, canary)
	salt := assertSalt(t, dir)
	_ = salt
	assertLogModes(t, dir)
}

// assertLogContent checks the INFO redaction and the DEBUG body.
func assertLogContent(t *testing.T, dir, canary string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "logs", "serve.log")) // #nosec G304 -- temp data dir
	if err != nil {
		t.Fatal(err)
	}
	var info, debug map[string]any
	for _, rec := range records(t, string(raw)) {
		switch rec["level"] {
		case "INFO":
			info = rec
		case "DEBUG":
			debug = rec
		}
	}
	if info == nil || debug == nil {
		t.Fatalf("missing INFO/DEBUG records: %s", raw)
	}
	if _, ok := info["text"]; ok {
		t.Fatalf("INFO line kept text: %v", info)
	}
	if id := mapStr(t, info, "chat_id"); strings.Contains(id, "123") {
		t.Fatalf("INFO line kept chat id: %v", info)
	}
	if body := mapStr(t, debug, "text"); body != canary {
		t.Fatalf("DEBUG body = %q, want canary", body)
	}
}

// assertSalt checks the per-install salt is 32 bytes and stable.
func assertSalt(t *testing.T, dir string) []byte {
	t.Helper()
	salt, err := LoadLogSalt(dir)
	if err != nil || len(salt) != 32 {
		t.Fatalf("salt = %d, %v", len(salt), err)
	}
	again, err := LoadLogSalt(dir)
	if err != nil || !bytes.Equal(salt, again) {
		t.Fatalf("salt not stable: %v", err)
	}
	return salt
}

// assertLogModes checks that every file exists and, on POSIX, that the
// 0700/0600 modes hold. Windows exposes no POSIX mode bits, so only existence
// is asserted there (production still creates the files with 0600/0700, which
// Windows maps to its own ACL).
func assertLogModes(t *testing.T, dir string) {
	t.Helper()
	paths := []string{
		filepath.Join(dir, "logs"),
		filepath.Join(dir, "log-salt"),
		filepath.Join(dir, "logs", "serve.log"),
	}
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s: %v", path, err)
		}
	}
	if runtime.GOOS == "windows" {
		return
	}
	for path, want := range map[string]os.FileMode{
		filepath.Join(dir, "logs"):              0o700,
		filepath.Join(dir, "log-salt"):          0o600,
		filepath.Join(dir, "logs", "serve.log"): 0o600,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode = %o, want %o", path, got, want)
		}
	}
}

// TestSetupLogRejectsBadInputs: unknown level, short salt and an unreadable
// salt fail closed instead of installing a weak handler.
func TestSetupLogRejectsBadInputs(t *testing.T) {
	dir := t.TempDir()
	if _, err := SetupLog(dir, "run", "trace", false, 0); err == nil {
		t.Fatal("unknown level accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, "log-salt"), []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := SetupLog(dir, "run", "info", false, 0); err == nil {
		t.Fatal("short salt accepted")
	}
}

// TestLogHandlerTTLWarningOnce: crossing the window emits exactly one WARN and
// still refuses the body.
func TestLogHandlerTTLWarningOnce(t *testing.T) {
	const canary = "TTL-EDGE-99"
	now := time.Now()
	buf, log := testLogger(t, LogOptions{MessageContent: true, TTL: time.Minute, Now: func() time.Time { return now }})
	now = now.Add(2 * time.Minute)
	for range 3 {
		log.Debug("late", "text", canary)
	}
	out := buf.String()
	if strings.Contains(out, canary) {
		t.Fatalf("body after ttl: %s", out)
	}
	if n := strings.Count(out, "log.message_content disabled after ttl"); n != 1 {
		t.Fatalf("warnings = %d", n)
	}
}
