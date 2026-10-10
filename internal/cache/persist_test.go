package cache

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testCache(t *testing.T, entries int, ttl int64) *Cache {
	t.Helper()
	return New(Config{Enabled: true, MaxEntries: entries, TTLSeconds: ttl, MaxBytes: 1 << 20, SlidingTTL: true})
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	src := testCache(t, 10, 3600)
	src.Put("k1", Entry{Body: []byte("one"), ContentType: "application/json", PromptTokens: 3})
	src.Put("k2", Entry{Body: []byte("two"), ContentType: "text/event-stream", SSE: true})

	snap := src.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("snapshot len = %d, want 2", len(snap))
	}

	dst := testCache(t, 10, 3600)
	restored, skipped := dst.Restore(snap)
	if restored != 2 || skipped != 0 {
		t.Fatalf("restored=%d skipped=%d, want 2/0", restored, skipped)
	}
	e, ok := dst.Get("k1")
	if !ok || string(e.Body) != "one" || e.PromptTokens != 3 {
		t.Fatalf("k1 not restored correctly: %+v ok=%v", e, ok)
	}
	e, ok = dst.Get("k2")
	if !ok || !e.SSE || e.ContentType != "text/event-stream" {
		t.Fatalf("k2 not restored correctly: %+v ok=%v", e, ok)
	}
}

func TestRestoreSkipsExpiredEmptyAndOversized(t *testing.T) {
	c := testCache(t, 10, 3600)
	restored, skipped := c.Restore([]Record{
		{Key: "expired", Body: []byte("x"), TTL: -time.Second},
		{Key: "empty", Body: nil, TTL: time.Minute},
		{Key: "huge", Body: make([]byte, MaxSingleEntryBytes+1), TTL: time.Minute},
		{Key: "good", Body: []byte("ok"), TTL: time.Minute},
	})
	if restored != 1 || skipped != 3 {
		t.Fatalf("restored=%d skipped=%d, want 1/3", restored, skipped)
	}
	if _, ok := c.Get("good"); !ok {
		t.Fatal("good entry should be present")
	}
}

func TestRestoreKeepsExistingEntry(t *testing.T) {
	c := testCache(t, 10, 3600)
	c.Put("k", Entry{Body: []byte("live")})
	restored, skipped := c.Restore([]Record{{Key: "k", Body: []byte("stale"), TTL: time.Minute}})
	if restored != 0 || skipped != 1 {
		t.Fatalf("restored=%d skipped=%d, want 0/1", restored, skipped)
	}
	e, _ := c.Get("k")
	if string(e.Body) != "live" {
		t.Fatalf("existing entry was overwritten: %q", e.Body)
	}
}

func TestRestoreRespectsMaxEntries(t *testing.T) {
	c := testCache(t, 2, 3600)
	c.Restore([]Record{
		{Key: "a", Body: []byte("a"), TTL: time.Minute},
		{Key: "b", Body: []byte("b"), TTL: time.Minute},
		{Key: "c", Body: []byte("c"), TTL: time.Minute},
	})
	if c.Len() > 2 {
		t.Fatalf("Len = %d, want <= 2 after restore", c.Len())
	}
}

func TestSaveLoadFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.gob.gz")
	src := testCache(t, 10, 3600)
	src.Put("k", Entry{Body: []byte("body"), ContentType: "application/json", PromptTokens: 7})

	if err := SaveFile(src, path); err != nil {
		t.Fatalf("SaveFile: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}

	dst := testCache(t, 10, 3600)
	restored, _, err := LoadFile(dst, path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if restored != 1 {
		t.Fatalf("restored = %d, want 1", restored)
	}
	e, ok := dst.Get("k")
	if !ok || string(e.Body) != "body" || e.PromptTokens != 7 {
		t.Fatalf("entry not restored: %+v ok=%v", e, ok)
	}
}

func TestSaveFileEmptyWritesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.gob.gz")
	if err := SaveFile(testCache(t, 10, 3600), path); err != nil {
		t.Fatalf("SaveFile: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("empty cache must not write a file (err=%v)", err)
	}
}

func TestLoadFileCorruptIsSafe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.gob.gz")
	if err := os.WriteFile(path, []byte("not a snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := testCache(t, 10, 3600)
	if _, _, err := LoadFile(c, path); err == nil {
		t.Fatal("corrupt file should error")
	}
	if c.Len() != 0 {
		t.Fatalf("corrupt load must leave the cache empty, got %d", c.Len())
	}
}
