package cache

import (
	"compress/gzip"
	"encoding/gob"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// cacheFileMagic prefixes every snapshot so a foreign or older file is
// rejected at load instead of decoded into garbage. Bump it when Record
// changes shape in a backwards-incompatible way.
const cacheFileMagic = "routre-cache-v1\n"

// Record is one cache entry in a snapshot. TTL is the remaining lifetime at
// snapshot time, so a restore preserves sliding-TTL semantics instead of
// resetting every entry to the configured maximum.
type Record struct {
	Key              string
	Body             []byte
	ContentType      string
	PromptTokens     int64
	CompletionTokens int64
	SSE              bool
	TTL              time.Duration
}

// Snapshot copies the live entries, skipping ones that have already expired.
// The copy is taken under the cache mutex; the bodies are shared (the caller
// must not mutate them).
func (c *Cache) Snapshot() []Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	out := make([]Record, 0, c.ll.Len())
	for el := c.ll.Front(); el != nil; el = el.Next() {
		it := el.Value.(*item)
		if !it.exp.After(now) {
			continue
		}
		out = append(out, Record{
			Key:              it.key,
			Body:             it.e.Body,
			ContentType:      it.e.ContentType,
			PromptTokens:     it.e.PromptTokens,
			CompletionTokens: it.e.CompletionTokens,
			SSE:              it.e.SSE,
			TTL:              it.exp.Sub(now),
		})
	}
	return out
}

// Restore inserts a snapshot, keeping existing entries (a live request always
// wins over a stale snapshot for the same key) and honouring the cache's
// capacity limits. It reports how many entries were restored and how many were
// skipped (expired, empty, oversized, or already present).
func (c *Cache) Restore(recs []Record) (restored, skipped int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.cfg.Enabled {
		return 0, len(recs)
	}
	now := time.Now()
	for _, r := range recs {
		if r.Key == "" || r.TTL <= 0 || len(r.Body) == 0 || len(r.Body) > MaxSingleEntryBytes {
			skipped++
			continue
		}
		if _, ok := c.m[r.Key]; ok {
			skipped++
			continue
		}
		el := c.ll.PushFront(&item{
			key: r.Key,
			e: Entry{
				Body:             r.Body,
				ContentType:      r.ContentType,
				PromptTokens:     r.PromptTokens,
				CompletionTokens: r.CompletionTokens,
				SSE:              r.SSE,
			},
			exp: now.Add(r.TTL),
		})
		c.m[r.Key] = el
		c.size += len(r.Body)
		restored++
	}
	for c.ll.Len() > c.cfg.MaxEntries || (c.cfg.MaxBytes > 0 && c.size > int(c.cfg.MaxBytes)) {
		c.remove(c.ll.Back())
	}
	return restored, skipped
}

// SaveFile writes a gzip-compressed gob snapshot atomically (temp file +
// rename, mode 0600). An empty cache writes nothing and removes no existing
// file: a restart with an empty live cache should not destroy the last warm
// snapshot.
func SaveFile(c *Cache, path string) error {
	recs := c.Snapshot()
	if len(recs) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(f)
	enc := gob.NewEncoder(gz)
	writeErr := func() error {
		if _, err := io.WriteString(gz, cacheFileMagic); err != nil {
			return err
		}
		return enc.Encode(recs)
	}()
	if writeErr == nil {
		writeErr = gz.Close()
	} else {
		_ = gz.Close()
	}
	if cerr := f.Close(); writeErr == nil {
		writeErr = cerr
	}
	if writeErr != nil {
		_ = os.Remove(tmp)
		return writeErr
	}
	return os.Rename(tmp, path)
}

// LoadFile restores a snapshot written by SaveFile. A missing file, a foreign
// magic, or a decode error is reported to the caller; the cache is left
// untouched in that case (callers start cold and log).
func LoadFile(c *Cache, path string) (restored, skipped int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return 0, 0, err
	}
	defer gz.Close()
	magic := make([]byte, len(cacheFileMagic))
	if _, err := io.ReadFull(gz, magic); err != nil {
		return 0, 0, err
	}
	if string(magic) != cacheFileMagic {
		return 0, 0, fmt.Errorf("cache: %s: not a routre cache snapshot", path)
	}
	var recs []Record
	if err := gob.NewDecoder(gz).Decode(&recs); err != nil {
		return 0, 0, err
	}
	restored, skipped = c.Restore(recs)
	return restored, skipped, nil
}
