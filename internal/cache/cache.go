// Package cache implements the response cache: an exact-match, keyed by
// SHA-256 of the processed (RTK-compressed, prefix-ordered) request body.
// It is intentionally a plain LRU with TTL — no vector DB, no embeddings —
// so it stays within the RAM budget.
package cache

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// Entry is a cached upstream response.
type Entry struct {
	Body        []byte
	ContentType string
}

// Config controls the cache. Zero value disables nothing by default;
// use DefaultConfig.
type Config struct {
	Enabled    bool  `json:"enabled"`
	MaxEntries int   `json:"max_entries"`
	TTLSeconds int64 `json:"ttl_seconds"`
	// PrefixOrder, when enabled, moves system messages to the front of the
	// request before keying, so that two requests differing only in message
	// order hit the same entry. It is also the prompt-cache-friendly ordering
	// (stable prefix first) for upstream providers.
	PrefixOrder bool `json:"prefix_order"`
	// MaxBytes caps the total RAM held by cached bodies (0 = unbounded).
	// Eviction is LRU: oldest entries drop first when the cap is exceeded.
	MaxBytes int64 `json:"max_bytes"`
}

// DefaultConfig targets a near-100% hit rate for repeat traffic: a large
// entry budget (4096), a long TTL (24h), and prefix ordering ON so that
// requests differing only in message order collide on the same key. The
// 64MiB byte cap keeps the cache bounded even with big responses cached.
func DefaultConfig() Config {
	return Config{Enabled: true, MaxEntries: 4096, TTLSeconds: 86400, PrefixOrder: true, MaxBytes: 64 << 20}
}

// Cache is a concurrency-safe exact-match LRU with TTL.
type Cache struct {
	mu   sync.Mutex
	cfg  Config
	ll   *list.List // front = most recently used
	m    map[string]*list.Element
	size int // bytes of cached bodies, for RAM accounting
}

type item struct {
	key string
	e   Entry
	exp time.Time
}

// New creates a cache with the given config.
func New(cfg Config) *Cache {
	return &Cache{cfg: cfg, ll: list.New(), m: make(map[string]*list.Element)}
}

// Update swaps config (SIGHUP reload). Entries are kept; eviction rules use
// the new limits on next write.
func (c *Cache) Update(cfg Config) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cfg = cfg
}

// Key returns the cache key for a processed request body. Always returns a
// key (even when disabled) so callers can log it.
func Key(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// Get returns the cached entry for key, if present and unexpired.
func (c *Cache) Get(key string) (Entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.cfg.Enabled {
		return Entry{}, false
	}
	el, ok := c.m[key]
	if !ok {
		return Entry{}, false
	}
	it := el.Value.(*item)
	if time.Now().After(it.exp) {
		c.remove(el)
		return Entry{}, false
	}
	c.ll.MoveToFront(el)
	return it.e, true
}

// Put stores an entry under key, enforcing TTL and LRU capacity. Oversized
// single entries are skipped (bounded RAM).
func (c *Cache) Put(key string, e Entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.cfg.Enabled {
		return
	}
	const maxSingle = 8 << 20 // 8 MiB per entry
	if len(e.Body) > maxSingle {
		return
	}
	now := time.Now()
	if el, ok := c.m[key]; ok {
		it := el.Value.(*item)
		c.size -= len(it.e.Body)
		it.e = e
		it.exp = now.Add(time.Duration(c.cfg.TTLSeconds) * time.Second)
		c.size += len(e.Body)
		c.ll.MoveToFront(el)
		return
	}
	el := c.ll.PushFront(&item{key: key, e: e, exp: now.Add(time.Duration(c.cfg.TTLSeconds) * time.Second)})
	c.m[key] = el
	c.size += len(e.Body)
	// Evict expired (from the back), then over-capacity (entries and
	// bytes, both from the back).
	for c.ll.Len() > 0 {
		back := c.ll.Back()
		if time.Now().Before(back.Value.(*item).exp) {
			break
		}
		c.remove(back)
	}
	for c.ll.Len() > c.cfg.MaxEntries || (c.cfg.MaxBytes > 0 && c.size > int(c.cfg.MaxBytes)) {
		c.remove(c.ll.Back())
	}
}

// SizeBytes reports the approximate RAM held by cached bodies.
func (c *Cache) SizeBytes() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.size
}

// Len reports the number of entries (for tests/status).
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}

// Clear empties the cache.
func (c *Cache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ll.Init()
	c.m = make(map[string]*list.Element)
	c.size = 0
}

func (c *Cache) remove(el *list.Element) {
	it := el.Value.(*item)
	c.ll.Remove(el)
	delete(c.m, it.key)
	c.size -= len(it.e.Body)
}
