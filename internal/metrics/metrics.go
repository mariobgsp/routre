// Package metrics implements a zero-dependency Prometheus-text metrics
// endpoint for the gateway. Counters are kept in plain maps guarded by a
// mutex; the /metrics handler renders them as exposition text, so no
// client library is needed (stdlib-only constraint).
package metrics

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

// Metrics is a concurrency-safe counter registry.
type Metrics struct {
	mu         sync.Mutex
	start      time.Time
	req        map[string]int64 // label: client|provider|model|class
	fail       map[string]int64 // label: provider|class
	cacheH     int64
	cacheM     int64
	cacheMBy   map[string]int64 // miss reason -> count
	rtkSave    int64
	rtkApplied int64
	cacheRd    int64
	cacheCr    int64            // prompt-cache creation tokens (1.25x writes), global sum
	cacheCrBy  map[string]int64 // per-provider creation tokens (provider -> tokens)
	cacheSv    int64            // estimated prompt-cache savings tokens, global sum
	cacheSvBy  map[string]int64 // per-provider net savings (provider -> net tokens)
	discTS     int64            // last successful model-discovery unix seconds (0 = never)
}

// New creates an empty registry with start time now.
func New() *Metrics {
	return &Metrics{
		start:     time.Now(),
		req:       map[string]int64{},
		fail:      map[string]int64{},
		cacheMBy:  map[string]int64{},
		cacheCrBy: map[string]int64{},
		cacheSvBy: map[string]int64{},
	}
}

// Request records one completed chat request (any outcome).
func (m *Metrics) Request(client, provider, model, class string) {
	label := strings.Join([]string{client, provider, model, class}, "|")
	m.mu.Lock()
	m.req[label]++
	m.mu.Unlock()
}

// Failure records one upstream failure that triggered failover.
func (m *Metrics) Failure(provider, class string) {
	label := provider + "|" + class
	m.mu.Lock()
	m.fail[label]++
	m.mu.Unlock()
}

// CacheHit records an exact-match cache hit; CacheMiss a miss.
func (m *Metrics) CacheHit() {
	m.mu.Lock()
	m.cacheH++
	m.mu.Unlock()
}

// CacheMiss records a cache miss.
func (m *Metrics) CacheMiss() {
	m.CacheMissReason("")
}

// CacheMissReason records a cache miss attributed to a reason
// (empty, absent, expired, shape_mismatch, disabled).
func (m *Metrics) CacheMissReason(reason string) {
	m.mu.Lock()
	m.cacheM++
	if reason != "" {
		m.cacheMBy[reason]++
	}
	m.mu.Unlock()
}

// RTKSaved adds saved-token counts from RTK compression.
func (m *Metrics) RTKSaved(n int64) {
	m.mu.Lock()
	m.rtkSave += n
	m.mu.Unlock()
}

// RTKApplied counts requests where RTK compression actually changed the
// payload (coverage metric).
func (m *Metrics) RTKApplied() {
	m.mu.Lock()
	m.rtkApplied++
	m.mu.Unlock()
}

// CacheRead adds provider-reported prompt-cache read tokens.
// The provider name is recorded as a label so the per-provider
// breakdown in /metrics can answer "which providers are benefiting
// from prompt caching" — most useful for Anthropic where the cache
// control injection is the explicit lever.
func (m *Metrics) CacheRead(provider string, n int64) {
	m.mu.Lock()
	m.cacheRd += n
	// A read token implies a 0.1x billing — savings vs full price are
	// 0.9 * n. Track this directly so /metrics shows the visible benefit
	// even when no pricing is configured (the dollar figure is reported
	// per-row in `routre list`).
	savings := n * 9 / 10
	m.cacheSv += savings
	m.cacheSvBy[provider] += savings
	m.mu.Unlock()
}

// CacheCreation adds provider-reported prompt-cache creation tokens
// (OpenAI / Anthropic `cache_creation_input_tokens`). These are billed
// at 1.25x — i.e. the write *cost* is 0.25x the token count beyond
// the standard input rate. Tracked as a separate counter so callers can
// see the full prompt-cache picture: how many tokens were reused (read)
// and how many were spent to make them reusable (creation).
func (m *Metrics) CacheCreation(provider string, n int64) {
	m.mu.Lock()
	m.cacheCr += n
	m.cacheCrBy[provider] += n
	// A creation token is an investment: it costs 0.25x extra now, but
	// the next 9 reads of the same prefix at 0.1x are pure savings.
	// We track the *gross write cost* here so callers can see the full
	// ledger; net (read savings minus write extra cost) is computed in
	// the usage store using configured provider prices.
	if n > 0 {
		m.cacheSvBy[provider] -= n / 4
	}
	m.mu.Unlock()
}

// WriteProm renders all counters in Prometheus text exposition format.
func (m *Metrics) WriteProm(w io.Writer) {
	m.mu.Lock()
	defer m.mu.Unlock()

	fmt.Fprintf(w, "# HELP routre_uptime_seconds seconds since gateway start\n")
	fmt.Fprintf(w, "# TYPE routre_uptime_seconds gauge\n")
	fmt.Fprintf(w, "routre_uptime_seconds %d\n", int64(time.Since(m.start).Seconds()))

	fmt.Fprintf(w, "# HELP routre_requests_total chat requests by client/provider/model/class\n")
	fmt.Fprintf(w, "# TYPE routre_requests_total counter\n")
	for _, k := range sortedKeys(m.req) {
		parts := strings.Split(k, "|")
		fmt.Fprintf(w, "routre_requests_total{client=%q,provider=%q,model=%q,class=%q} %d\n",
			parts[0], parts[1], parts[2], parts[3], m.req[k])
	}

	fmt.Fprintf(w, "# HELP routre_upstream_failures_total failovers by provider/class\n")
	fmt.Fprintf(w, "# TYPE routre_upstream_failures_total counter\n")
	for _, k := range sortedKeys(m.fail) {
		parts := strings.Split(k, "|")
		fmt.Fprintf(w, "routre_upstream_failures_total{provider=%q,class=%q} %d\n",
			parts[0], parts[1], m.fail[k])
	}

	fmt.Fprintf(w, "# HELP routre_cache_hits_total exact-match cache hits\n")
	fmt.Fprintf(w, "# TYPE routre_cache_hits_total counter\n")
	fmt.Fprintf(w, "routre_cache_hits_total %d\n", m.cacheH)
	fmt.Fprintf(w, "# HELP routre_cache_misses_total exact-match cache misses\n")
	fmt.Fprintf(w, "# TYPE routre_cache_misses_total counter\n")
	fmt.Fprintf(w, "routre_cache_misses_total %d\n", m.cacheM)
	fmt.Fprintf(w, "# HELP routre_cache_misses_by_reason_total exact-match cache misses by reason\n")
	fmt.Fprintf(w, "# TYPE routre_cache_misses_by_reason_total counter\n")
	for _, k := range sortedKeys(m.cacheMBy) {
		fmt.Fprintf(w, "routre_cache_misses_by_reason_total{reason=%q} %d\n", k, m.cacheMBy[k])
	}
	fmt.Fprintf(w, "# HELP routre_cache_hit_ratio hits / (hits+misses), 0 when idle\n")
	fmt.Fprintf(w, "# TYPE routre_cache_hit_ratio gauge\n")
	if total := m.cacheH + m.cacheM; total > 0 {
		fmt.Fprintf(w, "routre_cache_hit_ratio %.4f\n", float64(m.cacheH)/float64(total))
	} else {
		fmt.Fprintf(w, "routre_cache_hit_ratio 0\n")
	}
	fmt.Fprintf(w, "# HELP routre_rtk_applied_total requests where RTK changed the payload\n")
	fmt.Fprintf(w, "# TYPE routre_rtk_applied_total counter\n")
	fmt.Fprintf(w, "routre_rtk_applied_total %d\n", m.rtkApplied)
	fmt.Fprintf(w, "# HELP routre_rtk_saved_tokens_total tokens removed by RTK compression\n")
	fmt.Fprintf(w, "# TYPE routre_rtk_saved_tokens_total counter\n")
	fmt.Fprintf(w, "routre_rtk_saved_tokens_total %d\n", m.rtkSave)
	fmt.Fprintf(w, "# HELP routre_cache_read_tokens_total provider-reported prompt-cache hit tokens\n")
	fmt.Fprintf(w, "# TYPE routre_cache_read_tokens_total counter\n")
	fmt.Fprintf(w, "routre_cache_read_tokens_total %d\n", m.cacheRd)
	fmt.Fprintf(w, "# HELP routre_cache_creation_tokens_total provider-reported prompt-cache creation (write) tokens\n")
	fmt.Fprintf(w, "# TYPE routre_cache_creation_tokens_total counter\n")
	fmt.Fprintf(w, "routre_cache_creation_tokens_total %d\n", m.cacheCr)
	// Per-provider breakdown of cache creation so callers can see which
	// providers are paying the 1.25x write cost. The `_total` series
	// above is kept for backward compatibility; the per-provider lines
	// are additive and the per-provider values sum to the total.
	for _, k := range sortedKeys(m.cacheCrBy) {
		fmt.Fprintf(w, "routre_cache_creation_tokens_total{provider=%q} %d\n", k, m.cacheCrBy[k])
	}
	fmt.Fprintf(w, "# HELP routre_cache_savings_tokens_total estimated prompt-cache savings (read*0.9 minus write*0.25)\n")
	fmt.Fprintf(w, "# TYPE routre_cache_savings_tokens_total gauge\n")
	// Net token-equivalent savings: every read token saves 0.9 of a
	// full-price token, every creation token costs 0.25 of a full-price
	// token. The gauge below is the per-token net — positive means
	// caching is winning, negative means more is being written than
	// read on the running window.
	netSavings := m.cacheRd*9/10 - m.cacheCr/4
	fmt.Fprintf(w, "routre_cache_savings_tokens_total %d\n", netSavings)
	// Per-provider net savings so the Anthropic vs OpenAI prompt-cache
	// effect can be compared directly. Each provider's gauge is the
	// sum of its own read savings minus its own write extra cost.
	for _, k := range sortedKeys(m.cacheSvBy) {
		fmt.Fprintf(w, "routre_cache_savings_tokens_total{provider=%q} %d\n", k, m.cacheSvBy[k])
	}
	fmt.Fprintf(w, "# HELP routre_discovery_last_success_timestamp_seconds unix time of last successful model discovery (0 = never)\n")
	fmt.Fprintf(w, "# TYPE routre_discovery_last_success_timestamp_seconds gauge\n")
	fmt.Fprintf(w, "routre_discovery_last_success_timestamp_seconds %d\n", m.discTS)
}

// SetDiscoveryTimestamp records the last successful model-discovery run.
func (m *Metrics) SetDiscoveryTimestamp(t time.Time) {
	m.mu.Lock()
	m.discTS = t.Unix()
	m.mu.Unlock()
}

// DiscoveryTimestamp returns the last successful discovery unix seconds.
func (m *Metrics) DiscoveryTimestamp() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.discTS
}

// CacheMissByReason returns a copy of the per-reason miss counters.
func (m *Metrics) CacheMissByReason() map[string]int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]int64, len(m.cacheMBy))
	for k, v := range m.cacheMBy {
		out[k] = v
	}
	return out
}

// CacheHitRatio returns hits/(hits+misses); 0 when no traffic.
func (m *Metrics) CacheHitRatio() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if total := m.cacheH + m.cacheM; total > 0 {
		return float64(m.cacheH) / float64(total)
	}
	return 0
}

// RTKAppliedCount returns the number of requests where RTK changed the
// payload.
func (m *Metrics) RTKAppliedCount() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rtkApplied
}

func sortedKeys(m map[string]int64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
