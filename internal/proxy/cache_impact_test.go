package proxy

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mariobgsp/routre/internal/cache"
	"github.com/mariobgsp/routre/internal/config"
	"github.com/mariobgsp/routre/internal/mock"
	"github.com/mariobgsp/routre/internal/router"
	"github.com/mariobgsp/routre/internal/rtk"
	"github.com/mariobgsp/routre/internal/usage"
)

// cacheImpactEnv wires a gateway with one openai-kind mock upstream and the
// response cache enabled. Used by the cache-impact measurements below.
func cacheImpactEnv(b *testing.B) (string, *mock.Server) {
	b.Helper()
	b.Setenv("TEST_KEY_A", "test-key-a")
	m, err := mock.New("a")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(m.Close)
	tiers := `{"name":"t","providers":[{"name":"a","kind":"openai","base_url":"` + m.URL() + `/v1","api_key_env":"TEST_KEY_A","models":["m"]}]}`
	cfgJSON := `{"listen":"127.0.0.1:0","rtk":{"enabled":true,"min_bytes":500,"max_bytes":10485760},"cache":{"enabled":true,"max_entries":64,"ttl_seconds":3600},"tiers":[` + tiers + `]}`
	cfgPath := b.TempDir() + "/cfg.json"
	if err := os.WriteFile(cfgPath, []byte(cfgJSON), 0o644); err != nil {
		b.Fatal(err)
	}
	st := config.NewStore(cfgPath)
	if err := st.Load(); err != nil {
		b.Fatal(err)
	}
	cfg := st.Get()
	rtr := router.New(tiersFromConfig(cfg), router.DefaultCooldownPolicy())
	rtr.SetForwardUnknown(cfg.ForwardUnknown)
	cch := cache.New(cache.Config{Enabled: cfg.Cache.Enabled, MaxEntries: cfg.Cache.MaxEntries, TTLSeconds: cfg.Cache.TTLSeconds, PrefixOrder: cfg.Cache.PrefixOrder})
	tk := rtk.New(rtk.Config{Enabled: cfg.RTK.Enabled, MinBytes: cfg.RTK.MinBytes, MaxBytes: cfg.RTK.MaxBytes})
	h := NewHandlers(st, rtr, cch, tk, log.New(io.Discard, "", 0), usage.New(""))
	srv := New(h, log.New(io.Discard, "", 0))
	ln, err := srv.Listen("127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	b.Cleanup(func() { _ = srv.Shutdown(2 * time.Second) })
	return "http://" + ln.Addr().String(), m
}

// BenchmarkCacheMiss measures a full upstream round trip (mock, loopback) —
// the baseline every cache hit avoids.
func BenchmarkCacheMiss(b *testing.B) {
	base, _ := cacheImpactEnv(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Unique pad per iteration => guaranteed miss.
		doc, _ := json.Marshal(map[string]any{
			"model":    "m",
			"messages": []any{map[string]any{"role": "user", "content": "hello"}},
			"pad":      i,
		})
		resp, data := postB(b, base, "/v1/chat/completions", doc)
		if resp.StatusCode != 200 {
			b.Fatalf("status %d: %s", resp.StatusCode, data)
		}
	}
}

// BenchmarkCacheHit measures serving an identical request from the gateway's
// in-memory cache — no upstream call.
func BenchmarkCacheHit(b *testing.B) {
	base, m := cacheImpactEnv(b)
	body := chatBody(false, "")
	// Warm exactly one entry.
	if resp, _ := postB(b, base, "/v1/chat/completions", body); resp.Header.Get("X-Llrouter-Cache") != "miss" {
		b.Fatal("warmup must be a miss")
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp, data := postB(b, base, "/v1/chat/completions", body)
		if resp.Header.Get("X-Llrouter-Cache") != "hit" {
			b.Fatalf("expected hit, got %q (%s)", resp.Header.Get("X-Llrouter-Cache"), data)
		}
	}
	b.StopTimer()
	if got := m.Requests(); got != 1 {
		b.Fatalf("upstream called %d times, want 1", got)
	}
}

// streamCacheEnv wires a gateway with one openai-kind mock and cache
// enabled, for streaming-replay-cache tests.
func streamCacheEnv(t *testing.T, abortMid bool) (string, *mock.Server) {
	t.Helper()
	t.Setenv("TEST_KEY_A", "test-key-a")
	m, err := mock.New("a")
	if err != nil {
		t.Fatal(err)
	}
	m.SetAbortMid(abortMid)
	t.Cleanup(m.Close)
	tiers := `{"name":"t","providers":[{"name":"a","kind":"openai","base_url":"` + m.URL() + `/v1","api_key_env":"TEST_KEY_A","models":["m"]}]}`
	cfgJSON := `{"listen":"127.0.0.1:0","rtk":{"enabled":false},"cache":{"enabled":true,"max_entries":64,"ttl_seconds":3600},"tiers":[` + tiers + `]}`
	cfgPath := t.TempDir() + "/cfg.json"
	if err := os.WriteFile(cfgPath, []byte(cfgJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	st := config.NewStore(cfgPath)
	if err := st.Load(); err != nil {
		t.Fatalf("config load: %v", err)
	}
	cfg := st.Get()
	rtr := router.New(tiersFromConfig(cfg), router.DefaultCooldownPolicy())
	rtr.SetForwardUnknown(cfg.ForwardUnknown)
	cch := cache.New(cache.Config{Enabled: true, MaxEntries: 64, TTLSeconds: 3600})
	tk := rtk.New(rtk.Config{Enabled: false})
	logger := log.New(io.Discard, "", 0)
	h := NewHandlers(st, rtr, cch, tk, logger, usage.New(""))
	srv := New(h, logger)
	ln, err := srv.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Shutdown(2 * time.Second) })
	return "http://" + ln.Addr().String(), m
}

// TestStreamingCacheReplay: the second identical streaming request is
// replayed byte-for-byte from the gateway's cache; upstream called once.
func TestStreamingCacheReplay(t *testing.T) {
	base, m := streamCacheEnv(t, false)
	body := chatBody(true, "")
	resp1, data1 := post(t, base, "/v1/chat/completions", body)
	if resp1.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp1.StatusCode, data1)
	}
	if resp1.Header.Get("X-Llrouter-Cache") == "hit" {
		t.Fatal("first streaming request must reach upstream")
	}
	resp2, data2 := post(t, base, "/v1/chat/completions", body)
	if resp2.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp2.StatusCode, data2)
	}
	if resp2.Header.Get("X-Llrouter-Cache") != "hit" {
		t.Fatalf("second identical streaming request must be a replay hit; headers: %v\nbody:\n%s", resp2.Header, data2)
	}
	if string(data1) != string(data2) {
		t.Fatal("replayed stream must be byte-identical")
	}
	if got := m.Requests(); got != 1 {
		t.Fatalf("upstream must be called exactly once, got %d", got)
	}
}

// TestStreamingAbortNotCached: a mid-stream abort yields a truncated client
// stream that must never populate the replay cache.
func TestStreamingAbortNotCached(t *testing.T) {
	base, m := streamCacheEnv(t, true)
	body := chatBody(true, "")
	resp1, data1 := post(t, base, "/v1/chat/completions", body)
	if resp1.StatusCode != 200 || !strings.Contains(string(data1), "from-a") {
		t.Fatalf("expected partial stream from a, got %d: %s", resp1.StatusCode, data1)
	}
	resp2, _ := post(t, base, "/v1/chat/completions", body)
	if resp2.StatusCode != 200 {
		t.Fatalf("status %d", resp2.StatusCode)
	}
	if resp2.Header.Get("X-Llrouter-Cache") == "hit" {
		t.Fatal("aborted stream must not be cached")
	}
	if got := m.Requests(); got != 2 {
		t.Fatalf("upstream must serve both requests after abort, got %d", got)
	}
}

// postB is post() for benchmarks (Fatal on the testing.B).
func postB(b *testing.B, url, path string, body []byte) (*http.Response, []byte) {
	b.Helper()
	resp, err := http.Post(url+path, "application/json", strings.NewReader(string(body)))
	if err != nil {
		b.Fatalf("post: %v", err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, data
}

// BenchmarkCacheKeyVariance measures the hit rate when the same semantic
// request arrives with different JSON key order each time. With canonical
// keys enabled (the default) all variants must share one key and hit after
// warmup; without canonicalization each variant misses.
func BenchmarkCacheKeyVariance(b *testing.B) {
	base, m := cacheImpactEnv(b)
	// Warm one canonical entry.
	warm, _ := json.Marshal(map[string]any{
		"model": "m", "messages": []any{map[string]any{"role": "user", "content": "hello"}},
		"temperature": 0.7,
	})
	if resp, _ := postB(b, base, "/v1/chat/completions", warm); resp.Header.Get("X-Llrouter-Cache") != "miss" {
		b.Fatal("warmup must be a miss")
	}
	b.ResetTimer()
	// Rotate key order of the same fields each iteration (Go maps marshal
	// sorted, so build the variant manually to guarantee differing bytes).
	var sb strings.Builder
	for i := 0; i < b.N; i++ {
		sb.Reset()
		if i%2 == 0 {
			sb.WriteString(`{"model":"m","temperature":0.7,"messages":[{"content":"hello","role":"user"}]}`)
		} else {
			sb.WriteString(`{"messages":[{"role":"user","content":"hello"}],"temperature":0.7,"model":"m"}`)
		}
		resp, data := postB(b, base, "/v1/chat/completions", []byte(sb.String()))
		if resp.StatusCode != 200 {
			b.Fatalf("status %d: %s", resp.StatusCode, data)
		}
		if resp.Header.Get("X-Llrouter-Cache") != "hit" {
			b.Fatalf("key-order variant must hit canonical entry, got %q (%s)", resp.Header.Get("X-Llrouter-Cache"), data)
		}
	}
	b.StopTimer()
	if got := m.Requests(); got != 1 {
		b.Fatalf("upstream called %d times, want 1 (canonicalization must collapse variants)", got)
	}
}
