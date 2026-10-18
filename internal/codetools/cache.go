package codetools

import "time"

// Cache caps of CODE-TOOLS §7 (CT-35). Every hit is validated against a
// fresh stat; correctness never depends on invalidation.
const (
	readCacheCap  = 16 << 20 // file bytes ≤ readCacheFile + sha256
	readCacheFile = 2 << 20
	indexCacheCap = 4 << 20  // line-offset checkpoints
	snapshotCap   = 16 << 20 // stale_read diff snapshots, per session
	outlineCap    = 4 << 20
	idleRelease   = 10 * time.Minute
	racyWindow    = 2 * time.Second
)

// lru is a byte-budgeted LRU map over an intrusive ring (head.next = most recent).
type lru[K comparable, V any] struct {
	budget, used int
	head         lruNode[K, V]
	items        map[K]*lruNode[K, V]
}

type lruNode[K comparable, V any] struct {
	prev, next *lruNode[K, V]
	key        K
	val        V
	size       int
}

func newLRU[K comparable, V any](budget int) *lru[K, V] {
	c := &lru[K, V]{budget: budget, items: map[K]*lruNode[K, V]{}}
	c.head.prev, c.head.next = &c.head, &c.head
	return c
}

func (c *lru[K, V]) unlink(n *lruNode[K, V]) { n.prev.next, n.next.prev = n.next, n.prev }

func (c *lru[K, V]) pushFront(n *lruNode[K, V]) {
	n.prev, n.next = &c.head, c.head.next
	c.head.next.prev, c.head.next = n, n
}

func (c *lru[K, V]) get(k K) (V, bool) {
	n, ok := c.items[k]
	if !ok {
		var zero V
		return zero, false
	}
	c.unlink(n)
	c.pushFront(n)
	return n.val, true
}

// put stores v (size bytes); values larger than the budget are not kept.
func (c *lru[K, V]) put(k K, v V, size int) {
	c.remove(k)
	if size > c.budget {
		return
	}
	n := &lruNode[K, V]{key: k, val: v, size: size}
	c.items[k] = n
	c.pushFront(n)
	c.used += size
	for c.used > c.budget {
		c.remove(c.head.prev.key)
	}
}

func (c *lru[K, V]) remove(k K) {
	if n, ok := c.items[k]; ok {
		c.used -= n.size
		c.unlink(n)
		delete(c.items, k)
	}
}

// fileKey identifies one version of a file (CODE-TOOLS §7): device, inode,
// size and mtime; path disambiguates where the OS reports no inode.
type fileKey struct {
	path     string
	dev, ino uint64
	sdev     int64 // signed dev_t (darwin)
	size     int64
	mtimeNs  int64
	ctimeNs  int64
}

// readEntry caches a small file's bytes and digest.
type readEntry struct {
	sum  [32]byte
	data []byte
	at   time.Time // when hashed: racy entries (mtime within racyWindow) rehash
}

// tagEntry is what the session handed to the model as a tag (CODE-TOOLS §5.1).
type tagEntry struct {
	sum  [32]byte // full sha256 of bytes, or of the stat tuple (stat-tag)
	stat fileKey
	at   time.Time
	// full is the content sha256 of a stat-tagged file whose mtime was
	// racy (within racyWindow of the tag), taken when the tag was made.
	full    [32]byte
	hasFull bool
}

type caches struct {
	read     *lru[fileKey, readEntry]
	index    *lru[fileKey, *lineIndex]
	snaps    *lru[string, []byte] // tagKey → content at tag time (≤ readCacheFile)
	outlines *lru[fileKey, *outlined]
	tags     map[string]tagEntry // tagKey(resolved, tag)
}

func newCaches() *caches {
	return &caches{
		read:     newLRU[fileKey, readEntry](readCacheCap),
		index:    newLRU[fileKey, *lineIndex](indexCacheCap),
		snaps:    newLRU[string, []byte](snapshotCap),
		outlines: newLRU[fileKey, *outlined](outlineCap),
		tags:     map[string]tagEntry{},
	}
}

func tagKey(resolved, tag string) string { return resolved + "\x00" + tag }

// touch marks the Engine used and (re)arms the idle release timer: after
// idleRelease without a file tool call every cache is dropped; tags stay.
func (e *Engine) touch() {
	if e.idle == nil {
		e.idle = time.AfterFunc(idleRelease, e.releaseIdle)
		return
	}
	e.idle.Reset(idleRelease)
}

func (e *Engine) releaseIdle() {
	e.mu.Lock()
	defer e.mu.Unlock()
	tags := e.cache.tags
	e.cache = newCaches()
	e.cache.tags = tags
}
