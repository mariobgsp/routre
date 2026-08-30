package metrics

import (
	"bytes"
	"strings"
	"testing"
)

func TestCountersAndRatio(t *testing.T) {
	m := New()
	m.Request("codex", "p", "m", "ok")
	m.Request("codex", "p", "m", "ok")
	m.Failure("p", "server")
	m.CacheHit()
	m.CacheMiss()
	m.RTKSaved(100)
	m.RTKSaved(50)
	m.CacheRead("anthropic", 20)
	m.CacheRead("openai", 30)
	m.CacheCreation("anthropic", 10)
	m.RTKApplied()
	if r := m.CacheHitRatio(); r != 0.5 {
		t.Fatalf("hit ratio = %v, want 0.5", r)
	}
	if n := m.RTKAppliedCount(); n != 1 {
		t.Fatalf("rtk applied = %d", n)
	}
	var buf bytes.Buffer
	m.WriteProm(&buf)
	out := buf.String()
	for _, want := range []string{
		`routre_requests_total{client="codex",provider="p",model="m",class="ok"} 2`,
		`routre_upstream_failures_total{provider="p",class="server"} 1`,
		"routre_cache_hits_total 1",
		"routre_cache_misses_total 1",
		"routre_rtk_saved_tokens_total 150",
		// Global sum: 20 + 30 = 50 read tokens
		"routre_cache_read_tokens_total 50",
		// Anthropic creation: 10. Global sum: 10.
		"routre_cache_creation_tokens_total 10",
		`routre_cache_creation_tokens_total{provider="anthropic"} 10`,
		// Net (global) = 50*0.9 - 10*0.25 = 45 - 2 = 43
		"routre_cache_savings_tokens_total 43",
		// Per-provider net: anthropic = 20*0.9 - 10*0.25 = 18 - 2 = 16
		`routre_cache_savings_tokens_total{provider="anthropic"} 16`,
		// openai: 30*0.9 = 27 (no creation on openai in this test)
		`routre_cache_savings_tokens_total{provider="openai"} 27`,
		"routre_rtk_applied_total 1",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("Prom output missing %q\n%s", want, out)
		}
	}
}

func TestEmptyProm(t *testing.T) {
	m := New()
	var buf bytes.Buffer
	m.WriteProm(&buf)
	out := buf.String()
	if !strings.Contains(out, "routre_cache_hit_ratio 0") {
		t.Fatalf("expected ratio 0 when idle:\n%s", out)
	}
	if strings.Contains(out, "routre_requests_total{") {
		t.Fatalf("expected no request lines when idle:\n%s", out)
	}
	// No per-provider cache lines when nothing has been recorded.
	if strings.Contains(out, `routre_cache_creation_tokens_total{provider=`) {
		t.Fatalf("expected no per-provider cache lines when idle:\n%s", out)
	}
}

// TestPerProviderCacheLabels covers the per-provider label on
// cache_creation and cache_savings. Multiple providers' creation
// tokens are bucketed separately, and the per-provider savings gauge
// reflects each provider's own read-minus-creation math. The
// _total series stay as the global sum for backward compatibility.
func TestPerProviderCacheLabels(t *testing.T) {
	m := New()
	// Anthropic: 100 read + 40 creation. Per-provider net = 100*0.9 - 40*0.25 = 90 - 10 = 80.
	m.CacheRead("anthropic", 100)
	m.CacheCreation("anthropic", 40)
	// OpenAI: 50 read + 0 creation. Per-provider net = 50*0.9 = 45.
	m.CacheRead("openai", 50)
	// openrouter (the pre-existing traffic): 20 read + 0 creation. Net = 18.
	m.CacheRead("openrouter", 20)

	var buf bytes.Buffer
	m.WriteProm(&buf)
	out := buf.String()
	// Global totals: read 170, creation 40, net = 170*0.9 - 40*0.25 = 153 - 10 = 143
	for _, want := range []string{
		"routre_cache_read_tokens_total 170",
		"routre_cache_creation_tokens_total 40",
		`routre_cache_creation_tokens_total{provider="anthropic"} 40`,
		"routre_cache_savings_tokens_total 143",
		`routre_cache_savings_tokens_total{provider="anthropic"} 80`,
		`routre_cache_savings_tokens_total{provider="openai"} 45`,
		`routre_cache_savings_tokens_total{provider="openrouter"} 18`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("Prom output missing %q\n%s", want, out)
		}
	}
}
