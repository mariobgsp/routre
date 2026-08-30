package usage

import (
	"path/filepath"
	"testing"
)

func TestRecordAndSnapshot(t *testing.T) {
	s := New("")
	s.Record("openrouter", "tencent/hy3", 100, 50, 40, 0, Prices{InputPerMillion: 0.6, OutputPerMillion: 2.0}, 0)
	s.Record("openrouter", "tencent/hy3", 100, 50, 40, 0, Prices{InputPerMillion: 0.6, OutputPerMillion: 2.0}, 0)
	rows := s.Snapshot()
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	r := rows[0]
	if r.PromptTokens != 200 || r.CompletionTokens != 100 || r.Requests != 2 {
		t.Fatalf("aggregation wrong: %+v", r)
	}
	if r.RTKSavedTokens != 80 {
		t.Fatalf("rtk saved wrong: %+v", r)
	}
	// cost = 200/1e6*0.6 + 100/1e6*2.0 = 0.00012 + 0.0002 = 0.00032
	if got := r.CostUSD; got < 0.00031 || got > 0.00033 {
		t.Fatalf("cost wrong: %v", got)
	}
	// saved = 80/1e6*0.6 = 0.000048
	if got := r.SavedUSD; got < 0.000047 || got > 0.000049 {
		t.Fatalf("saved wrong: %v", got)
	}
}

// RecordFull must fold cache_creation into net prompt-cache savings
// at the provider's configured input price. Read savings = cacheRead
// * 0.9 * InputPerMillion / 1e6. Write extra cost = cacheCreation
// * 0.25 * InputPerMillion / 1e6. Net = read_savings - write_extra.
func TestRecordFullCacheSavingsNet(t *testing.T) {
	s := New("")
	// 1e6 read tokens + 0 creation -> 0.9 * 1.0 / 1 = 0.9 USD saved.
	s.RecordFull("openai", "gpt-4o", 1_000_000, 1000, 0, 0, 1_000_000, 0, Prices{InputPerMillion: 1.0, OutputPerMillion: 4.0}, 0)
	rows := s.Snapshot()
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if got := rows[0].CacheSavingsUSD; got < 0.899 || got > 0.901 {
		t.Fatalf("cache savings wrong: %v", got)
	}
	// Add a creation-heavy second row at the same price: 0 read +
	// 1e6 creation = -0.25 USD (1e6 * 0.25 * 1.0 / 1e6).
	s.RecordFull("openai", "gpt-4o", 1_000_000, 1000, 0, 0, 0, 1_000_000, Prices{InputPerMillion: 1.0, OutputPerMillion: 4.0}, 0)
	rows = s.Snapshot()
	if got := rows[0].CacheSavingsUSD; got < 0.649 || got > 0.651 {
		t.Fatalf("net savings wrong: %v", got)
	}
	// With no configured price the math must NOT invent a rate.
	s.RecordFull("mystery", "m", 100, 0, 0, 0, 100, 100, Prices{}, 0)
	rows = s.Snapshot()
	var got float64
	for _, r := range rows {
		if r.Provider == "mystery" {
			got = r.CacheSavingsUSD
		}
	}
	if got != 0 {
		t.Fatalf("expected 0 savings when price unknown, got %v", got)
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "usage.json")
	s := New(p)
	s.Record("a", "m1", 10, 20, 5, 3, Prices{}, 0)
	if err := s.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	s2, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	rows := s2.Snapshot()
	if len(rows) != 1 || rows[0].Model != "m1" || rows[0].PromptTokens != 10 {
		t.Fatalf("round trip failed: %+v", rows)
	}
	if rows[0].Requests != 1 {
		t.Fatalf("requests not persisted: %+v", rows[0])
	}
}

func TestLoadMissingIsEmpty(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("missing file must not error: %v", err)
	}
	if len(s.Snapshot()) != 0 {
		t.Fatal("expected empty store")
	}
}

func TestSeparateProviders(t *testing.T) {
	s := New("")
	s.Record("p1", "m", 1, 1, 0, 0, Prices{}, 0)
	s.Record("p2", "m", 1, 1, 0, 0, Prices{}, 0)
	if len(s.Snapshot()) != 2 {
		t.Fatal("providers must be tracked separately")
	}
}
