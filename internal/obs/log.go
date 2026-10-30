package obs

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// LogOptions configures the privacy handler. Zero values select the SPEC
// defaults (INFO, content off); Salt must be at least 16 random bytes.
type LogOptions struct {
	Level          slog.Level
	MessageContent bool
	// TTL is the content window measured from the first record; zero means
	// the SPEC default of 24 h. Values above 72 h are rejected by config
	// validation before this point.
	TTL  time.Duration
	Now  func() time.Time
	Salt []byte
}

// LogHandler is a slog.Handler that enforces SPEC §15 / GAP-SEC-11. It wraps
// the JSON handler, so records stay one JSON object per line.
type LogHandler struct {
	inner  slog.Handler
	opts   LogOptions
	start  time.Time
	warned atomic.Bool
}

// NewLogHandler wraps inner; Salt is required so identifiers can be keyed.
func NewLogHandler(inner slog.Handler, o LogOptions) (*LogHandler, error) {
	if len(o.Salt) < 16 {
		return nil, errors.New("obs: log salt must be at least 16 bytes")
	}
	if o.TTL <= 0 {
		o.TTL = 24 * time.Hour
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &LogHandler{inner: inner, opts: o, start: o.Now()}, nil
}

// Enabled reports whether the wrapped handler accepts l.
func (h *LogHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

// Handle applies the content policy and forwards the rebuilt record.
func (h *LogHandler) Handle(ctx context.Context, r slog.Record) error { //nolint:gocritic // slog.Handler fixes the by-value Record signature
	allowed := h.contentAllowed(r.Level)
	if h.opts.MessageContent && !allowed && !h.warned.Swap(true) {
		// The TTL expired while running: log once through the same handler so
		// the operator sees the switch back to content-free mode.
		w := slog.NewRecord(h.opts.Now(), slog.LevelWarn, "log.message_content disabled after ttl", 0)
		w.AddAttrs(slog.Duration("ttl", h.opts.TTL))
		if err := h.inner.Handle(ctx, w); err != nil {
			return fmt.Errorf("obs: log ttl warning: %w", err)
		}
	}
	out := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(h.clean(a, allowed)...)
		return true
	})
	if err := h.inner.Handle(ctx, out); err != nil {
		return fmt.Errorf("obs: log record: %w", err)
	}
	return nil
}

// WithAttrs cleans attrs once; later records inherit the same policy.
func (h *LogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	cleaned := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		cleaned = append(cleaned, h.clean(a, h.contentAllowed(slog.LevelInfo))...)
	}
	return &LogHandler{inner: h.inner.WithAttrs(cleaned), opts: h.opts, start: h.start}
}

// WithGroup passes the group through; Handle cleans its leaf attrs.
func (h *LogHandler) WithGroup(name string) slog.Handler {
	return &LogHandler{inner: h.inner.WithGroup(name), opts: h.opts, start: h.start}
}

func (h *LogHandler) contentAllowed(level slog.Level) bool {
	if !h.opts.MessageContent || level > slog.LevelDebug {
		return false
	}
	return h.opts.Now().Sub(h.start) < h.opts.TTL
}

// clean returns the replacement attrs of one input attr. Content keys keep
// only a length and a sha256; identifier keys keep only the keyed hash.
func (h *LogHandler) clean(a slog.Attr, contentAllowed bool) []slog.Attr {
	v := a.Value.Resolve()
	if v.Kind() == slog.KindGroup {
		group := v.Group()
		kept := make([]slog.Attr, 0, len(group))
		for _, child := range group {
			kept = append(kept, h.clean(child, contentAllowed)...)
		}
		return []slog.Attr{{Key: a.Key, Value: slog.GroupValue(kept...)}}
	}
	key := strings.ToLower(a.Key)
	if isContentKey(key) {
		if contentAllowed {
			return []slog.Attr{a}
		}
		return redactedAttrs(a.Key, v)
	}
	if isIDKey(key) && v.Kind() == slog.KindString {
		return []slog.Attr{slog.String(a.Key, h.hmac(v.String()))}
	}
	return []slog.Attr{a}
}

func (h *LogHandler) hmac(value string) string {
	m := hmac.New(sha256.New, h.opts.Salt)
	m.Write([]byte(value))
	return hex.EncodeToString(m.Sum(nil))[:12]
}

// redactedAttrs replaces a content value with <key>_len and <key>_sha. The
// value never reaches the writer, not even truncated.
func redactedAttrs(key string, v slog.Value) []slog.Attr {
	var raw []byte
	if s, ok := v.Any().(string); ok {
		raw = []byte(s)
	} else if b, ok := v.Any().([]byte); ok {
		raw = b
	} else {
		raw = []byte(fmt.Sprint(v.Any()))
	}
	sum := sha256.Sum256(raw)
	return []slog.Attr{
		slog.Int(key+"_len", len(raw)),
		slog.String(key+"_sha", hex.EncodeToString(sum[:])[:12]),
	}
}

// isContentKey is the normative SPEC §22.1 GAP-SEC-11 deny-list. A switch
// keeps it constant-free (no package-level mutable state) and allocation-free.
func isContentKey(k string) bool {
	switch k {
	case "args", "caption", "chat_title", "content", "first_name", "last_name",
		"message", "prompt", "query", "reply_to_text", "text", "username":
		return true
	}
	return false
}

// isIDKey lists the platform identifiers that are always keyed.
func isIDKey(k string) bool {
	switch k {
	case "chat_id", "sender", "user_id":
		return true
	}
	return false
}

// ParseLevel maps the SPEC §12 [log] level names to slog levels.
func ParseLevel(name string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("obs: unknown log level %q", name)
}

// SetupLog opens <dataDir>/logs/<name>.log (0600, dir 0700), installs the
// privacy handler as slog's default, and returns the close function. The salt
// lives in <dataDir>/log-salt and is shared by every log file of the install.
func SetupLog(dataDir, name, level string, content bool, ttl time.Duration) (func() error, error) {
	lvl, err := ParseLevel(level)
	if err != nil {
		return nil, err
	}
	salt, err := LoadLogSalt(dataDir)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(dataDir, "logs")
	if merr := os.MkdirAll(dir, 0o700); merr != nil {
		return nil, fmt.Errorf("obs: log dir: %w", merr)
	}
	f, err := os.OpenFile(filepath.Join(dir, name+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) // #nosec G304 -- <data_dir>/logs/<fixed name>
	if err != nil {
		return nil, fmt.Errorf("obs: log file: %w", err)
	}
	inner := slog.NewJSONHandler(f, &slog.HandlerOptions{Level: lvl})
	h, err := NewLogHandler(inner, LogOptions{Level: lvl, MessageContent: content, TTL: ttl, Salt: salt})
	if err != nil {
		return nil, errors.Join(err, f.Close())
	}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	if content {
		slog.Warn("log.message_content enabled", "ttl", ttl)
	}
	return func() error {
		slog.SetDefault(prev)
		return f.Close()
	}, nil
}

// LoadLogSalt returns the per-install 32-byte HMAC salt, creating it atomically
// (0600) on first use.
func LoadLogSalt(dataDir string) ([]byte, error) {
	path := filepath.Join(dataDir, "log-salt")
	if b, err := os.ReadFile(path); err == nil { // #nosec G304 -- per-install salt below <data_dir>
		if len(b) < 16 {
			return nil, fmt.Errorf("obs: log salt %s is too short", path)
		}
		return b, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("obs: log salt: %w", err)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("obs: data dir: %w", err)
	}
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("obs: log salt random: %w", err)
	}
	// O_EXCL keeps two processes from installing different salts; the loser
	// reads the winner's file.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 -- per-install salt path
	if errors.Is(err, os.ErrExist) {
		b, rerr := os.ReadFile(path) // #nosec G304 -- winner of the create race
		if rerr != nil {
			return nil, fmt.Errorf("obs: read log salt: %w", rerr)
		}
		return b, nil
	}
	if err != nil {
		return nil, fmt.Errorf("obs: create log salt: %w", err)
	}
	if _, err := f.Write(salt); err != nil {
		return nil, errors.Join(fmt.Errorf("obs: write log salt: %w", err), f.Close())
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("obs: close log salt: %w", err)
	}
	return salt, nil
}
