// Package usage tracks token consumption and savings per provider/model:
// tokens in/out, RTK compression savings, cache-hit savings, and estimated
// cost (input/output) with the saved amount. State is persisted as JSON to
// the data dir so `router-cli list` works across restarts.
package usage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Row is the aggregate for one provider+model pair.
type Row struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`

	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`

	// RTKSavedTokens: tokens removed by RTK compression before the request
	// reached the provider.
	RTKSavedTokens int64 `json:"rtk_saved_tokens"`
	// CacheSavedTokens: tokens that never reached any provider because the
	// exact-match cache served the response.
	CacheSavedTokens int64 `json:"cache_saved_tokens"`
	// CacheReadTokens: input tokens billed at the provider's prompt-cache
	// read rate (0.1x) instead of full price — provider-reported
	// prompt-cache hits. Zero when the provider does not report them.
	CacheReadTokens int64 `json:"cache_read_tokens"`

	// CostUSD: billed cost at the provider's price (0 = pricing unknown).
	CostUSD float64 `json:"cost_usd"`
	// SavedUSD: what the savings would have cost at the provider's price.
	SavedUSD float64 `json:"saved_usd"`

	Requests int64 `json:"requests"`
}

// Add merges another row into r.
func (r *Row) Add(o Row) {
	r.PromptTokens += o.PromptTokens
	r.CompletionTokens += o.CompletionTokens
	r.RTKSavedTokens += o.RTKSavedTokens
	r.CacheSavedTokens += o.CacheSavedTokens
	r.CacheReadTokens += o.CacheReadTokens
	r.CostUSD += o.CostUSD
	r.SavedUSD += o.SavedUSD
	r.Requests += o.Requests
}

// TotalTokens returns prompt + completion tokens.
func (r *Row) TotalTokens() int64 { return r.PromptTokens + r.CompletionTokens }

// TotalSavedTokens returns RTK + cache savings.
func (r *Row) TotalSavedTokens() int64 { return r.RTKSavedTokens + r.CacheSavedTokens }

// Prices are USD per 1M tokens (input/output). Zero = unknown.
type Prices struct {
	InputPerMillion  float64 `json:"input_per_million,omitempty"`
	OutputPerMillion float64 `json:"output_per_million,omitempty"`
}

// CostOf computes the cost of a usage delta at the given prices.
// It also reports the hypothetical cost of the saved tokens so callers can
// sum both sides with one helper.
func (p Prices) CostOf(prompt, completion, savedPrompt, savedCompletion int64) (cost, saved float64) {
	cost = float64(prompt)/1e6*p.InputPerMillion + float64(completion)/1e6*p.OutputPerMillion
	saved = float64(savedPrompt)/1e6*p.InputPerMillion + float64(savedCompletion)/1e6*p.OutputPerMillion
	return cost, saved
}

// Store is a concurrency-safe usage accumulator with optional JSON
// persistence.
type Store struct {
	mu   sync.Mutex
	rows map[string]*Row // key: provider + "\x00" + model
	path string
}

// New creates an empty store. path is the persistence file ("" = no
// persistence).
func New(path string) *Store {
	return &Store{rows: make(map[string]*Row), path: path}
}

// Load reads persisted usage from path (missing file = empty store).
func Load(path string) (*Store, error) {
	s := New(path)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("usage: read %s: %w", path, err)
	}
	var rows []*Row
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("usage: parse %s: %w", path, err)
	}
	for _, r := range rows {
		if r != nil && r.Provider != "" {
			s.rows[keyOf(r.Provider, r.Model)] = r
		}
	}
	return s, nil
}

// Record adds a usage delta. Cost is taken from providerReportedCost when
// the provider reports it (OpenRouter does); otherwise it is computed from
// the configured prices (zero prices = unknown, reported as n/a).
// cacheRead counts provider-reported prompt-cache hit tokens (billed at
// the discounted rate); they are not savings, just cheaper input.
func (s *Store) Record(provider, model string, prompt, completion int64, rtkSaved, cacheSaved int64, p Prices, providerReportedCost float64) {
	s.RecordFull(provider, model, prompt, completion, rtkSaved, cacheSaved, 0, p, providerReportedCost)
}

// RecordFull is Record plus provider-reported prompt-cache read tokens.
func (s *Store) RecordFull(provider, model string, prompt, completion int64, rtkSaved, cacheSaved, cacheRead int64, p Prices, providerReportedCost float64) {
	computedCost, saved := p.CostOf(prompt, completion, rtkSaved, cacheSaved)
	cost := computedCost
	if providerReportedCost > 0 {
		cost = providerReportedCost
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := keyOf(provider, model)
	row, ok := s.rows[k]
	if !ok {
		row = &Row{Provider: provider, Model: model}
		s.rows[k] = row
	}
	row.Add(Row{
		Provider: provider, Model: model,
		PromptTokens: prompt, CompletionTokens: completion,
		RTKSavedTokens: rtkSaved, CacheSavedTokens: cacheSaved,
		CacheReadTokens: cacheRead,
		CostUSD:         cost, SavedUSD: saved,
		Requests: 1,
	})
}

// Snapshot returns a copy of all rows.
func (s *Store) Snapshot() []Row {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Row, 0, len(s.rows))
	for _, r := range s.rows {
		c := *r
		out = append(out, c)
	}
	return out
}

// Save writes the snapshot to the persistence path (no-op if path empty).
func (s *Store) Save() error {
	if s.path == "" {
		return nil
	}
	s.mu.Lock()
	rows := make([]*Row, 0, len(s.rows))
	for _, r := range s.rows {
		c := *r
		rows = append(rows, &c)
	}
	s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	data, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func keyOf(provider, model string) string {
	return provider + "\x00" + model
}
