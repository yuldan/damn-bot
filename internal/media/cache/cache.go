// Package cache is the media cache and repair gateway (MEDIA-030). Entries are
// keyed by the SHA-256 of their content plus canonical parameters, blobs are
// deduplicated by content, eviction is deterministic (least recently used,
// then key), and a corrupt entry is deleted instead of being served. The
// gateway never extracts a document: it stores bytes and verifies their MIME.
package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/damnbot/damnbot/internal/modules/host"
)

// Cache bounds and eviction target (82% leaves headroom before the next put).
const (
	MaxEntryBytes = 32 << 20
	MaxEntries    = 64
	MaxBytes      = 256 << 20
	evictPercent  = 80
)

// ErrInvalid rejects a put or lookup outside the cache contract.
var ErrInvalid = errors.New("media/cache: invalid entry")

// Kinds is the closed entry class.
func validKind(kind string) bool {
	switch kind {
	case "image", "video", "audio", "document", "text":
		return true
	}
	return false
}

// PutRequest is one inbound media entry.
type PutRequest struct {
	Kind   string
	MIME   string
	Data   []byte
	Params map[string]string
}

// LookupRequest addresses an entry by content and parameters.
type LookupRequest struct {
	Kind   string
	MIME   string
	Data   []byte
	Params map[string]string
}

// Entry is the metadata of one cached value.
type Entry struct {
	Key    string
	SHA    string
	Kind   string
	MIME   string
	Bytes  int64
	UsedAt time.Time
}

type meta struct {
	Kind   string            `json:"kind"`
	MIME   string            `json:"mime"`
	SHA    string            `json:"sha"`
	Bytes  int64             `json:"bytes"`
	Params map[string]string `json:"params"`
	Seq    uint64            `json:"seq"`
}

// Store is one module-scoped cache. It is safe for concurrent use.
type Store struct {
	kv  host.Store
	mu  sync.Mutex
	now func() time.Time
}

// New binds the cache to the module store. A nil store fails closed.
func New(kv host.Store) (*Store, error) {
	if kv == nil {
		return nil, fmt.Errorf("%w: nil store", ErrInvalid)
	}
	return &Store{kv: kv, now: time.Now}, nil
}

// Key is the content+parameter key of one entry.
func Key(kind, mime string, data []byte, params map[string]string) (string, string, error) {
	if !validKind(kind) || len(data) == 0 || int64(len(data)) > MaxEntryBytes {
		return "", "", fmt.Errorf("%w: kind or size", ErrInvalid)
	}
	canon, err := canonicalParams(params)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	h := sha256.New()
	for _, part := range []string{"v1", kind, mime, sha, canon} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), sha, nil
}

// Put stores one entry and evicts deterministically when a bound is crossed.
// An existing blob is reused only after its hash matches; a corrupt blob is
// rewritten from the new data (repair on write).
func (s *Store) Put(ctx context.Context, req PutRequest) (Entry, error) {
	key, sha, err := Key(req.Kind, req.MIME, req.Data, req.Params)
	if err != nil {
		return Entry{}, err
	}
	mime, err := Sniff(req.Kind, req.MIME, req.Data)
	if err != nil {
		return Entry{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if perr := s.putBlob(ctx, sha, req.Data); perr != nil {
		return Entry{}, perr
	}
	seq, err := s.nextSeq(ctx)
	if err != nil {
		return Entry{}, err
	}
	m := meta{Kind: req.Kind, MIME: mime, SHA: sha, Bytes: int64(len(req.Data)), Params: clone(req.Params), Seq: seq}
	raw, err := json.Marshal(m)
	if err != nil {
		return Entry{}, fmt.Errorf("media/cache: encode entry: %w", err)
	}
	if err := s.kv.Put(ctx, entryKey(key), raw); err != nil {
		return Entry{}, fmt.Errorf("media/cache: put entry: %w", err)
	}
	if err := s.evict(ctx); err != nil {
		return Entry{}, err
	}
	return Entry{Key: key, SHA: sha, Kind: m.Kind, MIME: mime, Bytes: m.Bytes, UsedAt: s.now()}, nil
}

// Get returns a verified entry. A missing meta, malformed meta, missing blob
// or SHA mismatch is a miss; the corrupt pieces are deleted first.
func (s *Store) Get(ctx context.Context, key string) ([]byte, Entry, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, ok, err := s.kv.Get(ctx, entryKey(key))
	if err != nil {
		return nil, Entry{}, false, fmt.Errorf("media/cache: get entry: %w", err)
	}
	if !ok {
		return nil, Entry{}, false, nil
	}
	var m meta
	if json.Unmarshal(raw, &m) != nil || m.SHA == "" || !validKind(m.Kind) {
		return nil, Entry{}, false, s.remove(ctx, key, "")
	}
	data, ok, err := s.kv.Get(ctx, blobKey(m.SHA))
	if err != nil {
		return nil, Entry{}, false, fmt.Errorf("media/cache: get blob: %w", err)
	}
	if !ok || !sumMatches(data, m.SHA) {
		return nil, Entry{}, false, s.remove(ctx, key, m.SHA)
	}
	if seq, serr := s.nextSeq(ctx); serr == nil {
		m.Seq = seq
		if raw, merr := json.Marshal(m); merr == nil {
			if err := s.kv.Put(ctx, entryKey(key), raw); err != nil {
				return nil, Entry{}, false, fmt.Errorf("media/cache: touch entry: %w", err)
			}
		}
	}
	return data, Entry{Key: key, SHA: m.SHA, Kind: m.Kind, MIME: m.MIME, Bytes: m.Bytes, UsedAt: s.now()}, true, nil
}

// Lookup addresses an entry by its content and parameters.
func (s *Store) Lookup(ctx context.Context, req LookupRequest) ([]byte, Entry, bool, error) {
	key, _, err := Key(req.Kind, req.MIME, req.Data, req.Params)
	if err != nil {
		return nil, Entry{}, false, err
	}
	return s.Get(ctx, key)
}

// Repair deletes corrupt entries and unreferenced blobs. It is idempotent and
// returns the number of removed keys (entries plus orphans).
func (s *Store) Repair(ctx context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.entries(ctx)
	if err != nil {
		return 0, err
	}
	removed, live, err := s.repairEntries(ctx, entries)
	if err != nil {
		return removed, err
	}
	orphans, err := s.repairBlobs(ctx, live)
	return removed + orphans, err
}

func (s *Store) repairEntries(ctx context.Context, entries map[string]meta) (int, map[string]bool, error) {
	removed := 0
	live := map[string]bool{}
	for key, m := range entries {
		if !validKind(m.Kind) || m.SHA == "" {
			if err := s.remove(ctx, key, ""); err != nil {
				return removed, live, err
			}
			removed++
			continue
		}
		data, ok, err := s.kv.Get(ctx, blobKey(m.SHA))
		if err != nil {
			return removed, live, fmt.Errorf("media/cache: repair blob: %w", err)
		}
		if !ok || !sumMatches(data, m.SHA) {
			if err := s.remove(ctx, key, m.SHA); err != nil {
				return removed, live, err
			}
			removed++
			continue
		}
		live[m.SHA] = true
	}
	return removed, live, nil
}

func (s *Store) repairBlobs(ctx context.Context, live map[string]bool) (int, error) {
	blobs, err := s.kv.List(ctx, []byte("blob/"))
	if err != nil {
		return 0, fmt.Errorf("media/cache: list blobs: %w", err)
	}
	removed := 0
	for _, key := range blobs {
		sha := strings.TrimPrefix(string(key), "blob/")
		if live[sha] {
			continue
		}
		if err := s.kv.Delete(ctx, key); err != nil {
			return removed, fmt.Errorf("media/cache: delete orphan: %w", err)
		}
		removed++
	}
	return removed, nil
}

func (s *Store) putBlob(ctx context.Context, sha string, data []byte) error {
	if old, ok, err := s.kv.Get(ctx, blobKey(sha)); err != nil {
		return fmt.Errorf("media/cache: read blob: %w", err)
	} else if ok && sumMatches(old, sha) {
		return nil
	}
	if err := s.kv.Put(ctx, blobKey(sha), data); err != nil {
		return fmt.Errorf("media/cache: put blob: %w", err)
	}
	return nil
}

func (s *Store) remove(ctx context.Context, key, sha string) error {
	if err := s.kv.Delete(ctx, entryKey(key)); err != nil {
		return fmt.Errorf("media/cache: delete entry: %w", err)
	}
	if sha != "" {
		// The blob is content-addressed; only remove it when the corrupt
		// content itself is what is being discarded.
		if err := s.kv.Delete(ctx, blobKey(sha)); err != nil {
			return fmt.Errorf("media/cache: delete blob: %w", err)
		}
	}
	return nil
}

func (s *Store) entries(ctx context.Context) (map[string]meta, error) {
	keys, err := s.kv.List(ctx, []byte("entry/"))
	if err != nil {
		return nil, fmt.Errorf("media/cache: list entries: %w", err)
	}
	out := make(map[string]meta, len(keys))
	for _, rawKey := range keys {
		key := strings.TrimPrefix(string(rawKey), "entry/")
		raw, ok, err := s.kv.Get(ctx, rawKey)
		if err != nil {
			return nil, fmt.Errorf("media/cache: get entry: %w", err)
		}
		if !ok {
			continue
		}
		var m meta
		if json.Unmarshal(raw, &m) != nil {
			m = meta{}
		}
		out[key] = m
	}
	return out, nil
}

func (s *Store) nextSeq(ctx context.Context) (uint64, error) {
	raw, ok, err := s.kv.Get(ctx, []byte("seq"))
	if err != nil {
		return 0, fmt.Errorf("media/cache: seq: %w", err)
	}
	var n uint64
	if ok {
		parsed, perr := strconv.ParseUint(string(raw), 10, 64)
		if perr != nil {
			return 0, fmt.Errorf("media/cache: corrupt seq: %w", perr)
		}
		n = parsed
	}
	n++
	if err := s.kv.Put(ctx, []byte("seq"), []byte(strconv.FormatUint(n, 10))); err != nil {
		return 0, fmt.Errorf("media/cache: seq: %w", err)
	}
	return n, nil
}

// evict keeps the cache under MaxEntries and MaxBytes by removing the least
// recently used entries, breaking ties by key so the outcome is deterministic.
func (s *Store) evict(ctx context.Context) error {
	entries, err := s.entries(ctx)
	if err != nil {
		return err
	}
	unique := uniqueBytes(entries)
	if len(entries) <= MaxEntries && total(unique) <= MaxBytes {
		return nil
	}
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := entries[keys[i]], entries[keys[j]]
		if a.Seq != b.Seq {
			return a.Seq < b.Seq
		}
		return keys[i] < keys[j]
	})
	return s.evictLocked(ctx, entries, unique, keys)
}

func uniqueBytes(entries map[string]meta) map[string]int64 {
	unique := map[string]int64{}
	for _, m := range entries {
		if m.SHA != "" {
			unique[m.SHA] = m.Bytes
		}
	}
	return unique
}

func (s *Store) evictLocked(ctx context.Context, entries map[string]meta, unique map[string]int64, keys []string) error {
	entryTarget := MaxEntries * evictPercent / 100
	byteTarget := int64(MaxBytes) * evictPercent / 100
	for _, key := range keys {
		if len(entries) <= entryTarget && total(unique) <= byteTarget {
			return nil
		}
		if err := s.evictOne(ctx, entries, unique, key); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) evictOne(ctx context.Context, entries map[string]meta, unique map[string]int64, key string) error {
	m := entries[key]
	if err := s.kv.Delete(ctx, entryKey(key)); err != nil {
		return fmt.Errorf("media/cache: evict entry: %w", err)
	}
	delete(entries, key)
	orphan := true
	for _, other := range entries {
		if other.SHA == m.SHA {
			orphan = false
			break
		}
	}
	if !orphan {
		return nil
	}
	delete(unique, m.SHA)
	if m.SHA == "" {
		return nil
	}
	if err := s.kv.Delete(ctx, blobKey(m.SHA)); err != nil {
		return fmt.Errorf("media/cache: evict blob: %w", err)
	}
	return nil
}

func total(unique map[string]int64) int64 {
	var n int64
	for _, size := range unique {
		n += size
	}
	return n
}

// Sniff validates the declared MIME against the content class.
func Sniff(kind, mime string, data []byte) (string, error) {
	if !validKind(kind) || len(data) == 0 {
		return "", fmt.Errorf("%w: kind or empty data", ErrInvalid)
	}
	sniff := http.DetectContentType(data)
	switch kind {
	case "image":
		if !strings.HasPrefix(sniff, "image/") {
			return "", fmt.Errorf("%w: not an image (%s)", ErrInvalid, sniff)
		}
	case "video":
		if !strings.HasPrefix(sniff, "video/") {
			return "", fmt.Errorf("%w: not a video (%s)", ErrInvalid, sniff)
		}
	case "audio":
		if !strings.HasPrefix(sniff, "audio/") {
			return "", fmt.Errorf("%w: not audio (%s)", ErrInvalid, sniff)
		}
	case "document":
		if !strings.HasPrefix(sniff, "application/") && !strings.HasPrefix(sniff, "text/") {
			return "", fmt.Errorf("%w: not a document (%s)", ErrInvalid, sniff)
		}
	default:
		if !strings.HasPrefix(sniff, "text/") {
			return "", fmt.Errorf("%w: not text (%s)", ErrInvalid, sniff)
		}
	}
	if mime != "" && !strings.HasPrefix(mime, strings.SplitN(sniff, ";", 2)[0]) {
		return "", fmt.Errorf("%w: declared %s, content is %s", ErrInvalid, mime, sniff)
	}
	return sniff, nil
}

func sumMatches(data []byte, sha string) bool {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]) == sha
}

func canonicalParams(params map[string]string) (string, error) {
	if len(params) > 16 {
		return "", fmt.Errorf("%w: too many params", ErrInvalid)
	}
	keys := make([]string, 0, len(params))
	for k, v := range params {
		if k == "" || strings.ContainsAny(k+v, "\x00\n") || len(k)+len(v) > 1024 {
			return "", fmt.Errorf("%w: param", ErrInvalid)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(params[k])
		b.WriteByte('\n')
	}
	return b.String(), nil
}

func clone(params map[string]string) map[string]string {
	out := make(map[string]string, len(params))
	maps.Copy(out, params)
	return out
}

func entryKey(key string) []byte { return []byte("entry/" + key) }
func blobKey(sha string) []byte  { return []byte("blob/" + sha) }
