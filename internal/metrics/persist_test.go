package metrics

import (
	"path/filepath"
	"testing"
)

func TestCountersRoundTrip(t *testing.T) {
	m := New()
	m.CacheHit()
	m.CacheMissReason("absent")
	m.RTKApplied()
	m.RTKSaved(120)
	m.RTKPartial()
	m.CacheRead("p", 80)
	m.CacheCaptureSkipped("truncated")

	path := filepath.Join(t.TempDir(), "counters.json")
	if err := SaveCounters(m, path); err != nil {
		t.Fatalf("SaveCounters: %v", err)
	}
	c, err := LoadCounters(path)
	if err != nil {
		t.Fatalf("LoadCounters: %v", err)
	}
	if c.CacheHits != 1 || c.CacheMisses != 1 || c.CacheMissByReason["absent"] != 1 {
		t.Fatalf("cache counters wrong: %+v", c)
	}
	if c.RTKSavedTokens != 120 || c.RTKApplied != 1 || c.RTKPartial != 1 {
		t.Fatalf("rtk counters wrong: %+v", c)
	}
	if c.CacheReadTokens != 80 {
		t.Fatalf("cache read tokens = %d, want 80", c.CacheReadTokens)
	}
	if c.CacheCaptureSkipped["truncated"] != 1 {
		t.Fatalf("capture-skip counters wrong: %+v", c)
	}

	dst := New()
	dst.RestoreCounters(c)
	if got := dst.CacheHitRatio(); got != 0.5 {
		t.Fatalf("restored hit ratio = %v, want 0.5", got)
	}
}
