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

// TestStreamingNeverCaches documents today's gap: identical streaming
// requests always re-hit the upstream because the response cache is
// non-streaming only.
func TestStreamingNeverCaches(t *testing.T) {
	base, m := func() (string, *mock.Server) {
		t.Helper()
		t.Setenv("TEST_KEY_A", "test-key-a")
		m, err := mock.New("a")
		if err != nil {
			t.Fatal(err)
		}
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
	}()

	body := chatBody(true, "")
	for i := 0; i < 2; i++ {
		resp, data := post(t, base, "/v1/chat/completions", body)
		if resp.StatusCode != 200 {
			t.Fatalf("status %d: %s", resp.StatusCode, data)
		}
	}
	if got := m.Requests(); got != 2 {
		t.Fatalf("streaming requests must each reach upstream; got %d calls, want 2 (proves streaming bypasses cache)", got)
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
