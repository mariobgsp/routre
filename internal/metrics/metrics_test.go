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
	m.CacheRead(20)
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
		"routre_cache_read_tokens_total 20",
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
}
