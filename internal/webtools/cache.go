//go:build !minimal

package webtools

import (
	"container/list"
	"sync"
	"time"
)

// cache is a bounded LRU with per-entry TTL and single-flight loads. Entries
// are keyed by a tenant-scoped digest, so two sessions never share a hit
// (GAP-COST-12).
type cache struct {
	mu         sync.Mutex
	entries    map[string]*cacheEntry
	lru        *list.List
	bytes      int
	maxEntries int
	maxBytes   int
	now        func() time.Time
	calls      map[string]*cacheCall
}

type cacheEntry struct {
	value   any
	size    int
	expires time.Time
	elem    *list.Element
}

type cacheCall struct {
	done  chan struct{}
	value any
	size  int
	err   error
}

func newCache(maxEntries, maxBytes int, now func() time.Time) *cache {
	if maxEntries <= 0 {
		maxEntries = 500
	}
	if maxBytes <= 0 {
		maxBytes = 32 << 20
	}
	if now == nil {
		now = time.Now
	}
	return &cache{
		entries: make(map[string]*cacheEntry), lru: list.New(),
		maxEntries: maxEntries, maxBytes: maxBytes, now: now,
		calls: make(map[string]*cacheCall),
	}
}

func (c *cache) get(key string) (any, bool) {
	e, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if !c.now().Before(e.expires) {
		c.remove(key, e)
		return nil, false
	}
	c.lru.MoveToFront(e.elem)
	return e.value, true
}

func (c *cache) put(key string, value any, size int, ttl time.Duration) {
	if size > c.maxBytes {
		return
	}
	if old, ok := c.entries[key]; ok {
		c.remove(key, old)
	}
	e := &cacheEntry{value: value, size: size, expires: c.now().Add(ttl)}
	e.elem = c.lru.PushFront(key)
	c.entries[key] = e
	c.bytes += size
	for len(c.entries) > c.maxEntries || c.bytes > c.maxBytes {
		back := c.lru.Back()
		if back == nil {
			break
		}
		key, ok := back.Value.(string)
		if !ok {
			break
		}
		if old, ok := c.entries[key]; ok {
			c.remove(key, old)
		}
	}
}

func (c *cache) remove(key string, e *cacheEntry) {
	c.lru.Remove(e.elem)
	delete(c.entries, key)
	c.bytes -= e.size
}

// load returns a hit, or runs fn once for concurrent callers with the same
// key. The second return reports whether no backend call was made by this
// caller (cache hit or coalesced single-flight).
func (c *cache) load(key string, ttl time.Duration, fn func() (any, int, error)) (any, bool, error) {
	c.mu.Lock()
	if v, ok := c.get(key); ok {
		c.mu.Unlock()
		return v, true, nil
	}
	if call, ok := c.calls[key]; ok {
		c.mu.Unlock()
		<-call.done
		return call.value, true, call.err
	}
	call := &cacheCall{done: make(chan struct{})}
	c.calls[key] = call
	c.mu.Unlock()

	call.value, call.size, call.err = fn()

	c.mu.Lock()
	if call.err == nil {
		c.put(key, call.value, call.size, ttl)
	}
	delete(c.calls, key)
	c.mu.Unlock()
	close(call.done)
	return call.value, false, call.err
}

func (c *cache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}
