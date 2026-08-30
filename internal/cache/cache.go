// Package cache implements the response cache: an exact-match, keyed by
// SHA-256 of the processed (RTK-compressed, prefix-ordered) request body.
// It is intentionally a plain LRU with TTL — no vector DB, no embeddings —
// so it stays within the RAM budget.
package cache

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"github.com/mariobgsp/routre/internal/config"
	"sync"
	"time"
)

// MissReason classifies why Get did not serve an entry. Used for
// per-reason miss metrics so the dominant miss cause is observable.
type MissReason string

const (
	// MissDisabled: cache disabled; Get never serves.
	MissDisabled MissReason = "disabled"
	// MissAbsent: no entry under the key (never cached, evicted, or
	// wiped by restart).
	MissAbsent MissReason = "absent"
	// MissExpired: an entry existed but its TTL elapsed; removed on read.
	MissExpired MissReason = "expired"
)

// Entry is a cached upstream response. PromptTokens/CompletionTokens are
// the upstream-reported usage of the request that produced the response;
// cache hits report them so the ledger matches the provider's counts
// instead of the gateway's length-based estimates.
type Entry struct {
	Body             []byte
	ContentType      string
	PromptTokens     int64
	CompletionTokens int64
	// SSE marks an entry captured from a streaming relay (client-dialect SSE
	// frames). Only SSE-marked entries are replayed on the streaming path,
	// and JSON entries are never served to streaming clients — the same key
	// can legitimately hold either shape depending on how the first request
	// arrived.
	SSE bool
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
	// SlidingTTL, when true, refreshes an entry's expiry on every hit
	// (now + TTL), so actively used entries never expire. Hot repeat
	// traffic stops missing once/day.
	SlidingTTL bool `json:"sliding_ttl"`
}

// DefaultConfig targets a near-100% hit rate for repeat traffic: a large
// entry budget (16384), a long TTL (7 days), sliding TTL so hot entries
// never expire, and prefix ordering ON so that requests differing only in
// message order collide on the same key. The 128MiB byte cap keeps the
// cache bounded even with big responses cached.
func DefaultConfig() Config {
	return Config{Enabled: true, MaxEntries: 16384, TTLSeconds: 604800, PrefixOrder: true, MaxBytes: 128 << 20, SlidingTTL: true}
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
func (c *Cache) Reconfigure(cfg config.Config) {
	c.Update(Config{
		Enabled:     cfg.Cache.Enabled,
		MaxEntries:  cfg.Cache.MaxEntries,
		TTLSeconds:  cfg.Cache.TTLSeconds,
		PrefixOrder: cfg.Cache.PrefixOrder,
		MaxBytes:    cfg.Cache.MaxBytes,
		SlidingTTL:  cfg.Cache.SlidingTTL,
	})
}

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
// A hit moves the entry to the LRU front.
func (c *Cache) Get(key string) (Entry, bool) {
	e, ok, _ := c.GetWithReason(key)
	return e, ok
}

// GetWithReason returns the cached entry for key, if present and
// unexpired, plus a miss reason (disabled / absent / expired) when it
// is not served. A hit moves the entry to the LRU front.
func (c *Cache) GetWithReason(key string) (Entry, bool, MissReason) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.cfg.Enabled {
		return Entry{}, false, MissDisabled
	}
	el, ok := c.m[key]
	if !ok {
		return Entry{}, false, MissAbsent
	}
	it := el.Value.(*item)
	if time.Now().After(it.exp) {
		c.remove(el)
		return Entry{}, false, MissExpired
	}
	// SlidingTTL: on hit, refresh expiry to now + TTL so hot entries
	// never expire while actively used.
	if c.cfg.SlidingTTL {
		it.exp = time.Now().Add(time.Duration(c.cfg.TTLSeconds) * time.Second)
	}
	c.ll.MoveToFront(el)
	return it.e, true, ""
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
