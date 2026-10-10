package metrics

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Counters is the durable subset of the registry: the lifetime savings and
// cache numbers the dashboard and CLI report. Request/failure maps are
// deliberately excluded — they are debugging detail, not a user-facing total.
type Counters struct {
	CacheHits           int64            "json:\"cache_hits\""
	CacheMisses         int64            "json:\"cache_misses\""
	CacheMissByReason   map[string]int64 "json:\"cache_misses_by_reason,omitempty\""
	CacheCaptureSkipped map[string]int64 "json:\"cache_capture_skipped,omitempty\""
	RTKSavedTokens      int64            "json:\"rtk_saved_tokens\""
	RTKApplied          int64            "json:\"rtk_applied\""
	RTKPartial          int64            "json:\"rtk_partial\""
	CacheReadTokens     int64            "json:\"cache_read_tokens\""
	CacheCreateTokens   int64            "json:\"cache_creation_tokens\""
	CacheSavingsTokens  int64            "json:\"cache_savings_tokens\""
}

// SnapshotCounters copies the durable counters.
func (m *Metrics) SnapshotCounters() Counters {
	m.mu.Lock()
	defer m.mu.Unlock()
	return Counters{
		CacheHits:           m.cacheH,
		CacheMisses:         m.cacheM,
		CacheMissByReason:   copyMap(m.cacheMBy),
		CacheCaptureSkipped: copyMap(m.cacheCapSkip),
		RTKSavedTokens:      m.rtkSave,
		RTKApplied:          m.rtkApplied,
		RTKPartial:          m.rtkPartial,
		CacheReadTokens:     m.cacheRd,
		CacheCreateTokens:   m.cacheCr,
		CacheSavingsTokens:  m.cacheSv,
	}
}

// RestoreCounters adds a persisted snapshot to the live counters (a fresh
// process starts at zero, so this is a restore; adding rather than assigning
// keeps any traffic that already arrived from being lost).
func (m *Metrics) RestoreCounters(c Counters) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cacheH += c.CacheHits
	m.cacheM += c.CacheMisses
	for k, v := range c.CacheMissByReason {
		m.cacheMBy[k] += v
	}
	for k, v := range c.CacheCaptureSkipped {
		m.cacheCapSkip[k] += v
	}
	m.rtkSave += c.RTKSavedTokens
	m.rtkApplied += c.RTKApplied
	m.rtkPartial += c.RTKPartial
	m.cacheRd += c.CacheReadTokens
	m.cacheCr += c.CacheCreateTokens
	m.cacheSv += c.CacheSavingsTokens
}

// SaveCounters writes the snapshot as JSON (0600, atomic temp+rename).
func SaveCounters(m *Metrics, path string) error {
	data, err := json.Marshal(m.SnapshotCounters())
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadCounters reads a snapshot written by SaveCounters.
func LoadCounters(path string) (Counters, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Counters{}, err
	}
	var c Counters
	if err := json.Unmarshal(data, &c); err != nil {
		return Counters{}, err
	}
	return c, nil
}

func copyMap(in map[string]int64) map[string]int64 {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]int64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
