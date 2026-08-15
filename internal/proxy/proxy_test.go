package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"routre-cli/internal/cache"
	"routre-cli/internal/config"
	"routre-cli/internal/mock"
	"routre-cli/internal/router"
	"routre-cli/internal/rtk"
	"routre-cli/internal/usage"
)

// testEnv wires a full gateway against mock upstreams and returns its base
// URL. t.Cleanup closes everything.
func testEnv(t *testing.T, cfgJSON string) (base string, mocks map[string]*mock.Server) {
	t.Helper()
	// The gateway holds provider keys; every provider referenced by the
	// test configs needs its env var set.
	t.Setenv("TEST_KEY_A", "test-key-a")
	t.Setenv("TEST_KEY_B", "test-key-b")
	t.Setenv("TEST_KEY_C", "test-key-c")
	cfgPath := writeConfigFile(t, cfgJSON)
	st := config.NewStore(cfgPath)
	if err := st.Load(); err != nil {
		t.Fatalf("config load: %v", err)
	}
	cfg := st.Get()

	rtr := router.New(tiersFromConfig(cfg), router.DefaultCooldownPolicy())
	cch := cache.New(cache.Config{
		Enabled: cfg.Cache.Enabled, MaxEntries: cfg.Cache.MaxEntries,
		TTLSeconds: cfg.Cache.TTLSeconds, PrefixOrder: cfg.Cache.PrefixOrder,
	})
	tk := rtk.New(rtk.Config{Enabled: cfg.RTK.Enabled, MinBytes: cfg.RTK.MinBytes, MaxBytes: cfg.RTK.MaxBytes})
	logger := log.New(io.Discard, "", 0)
	h := NewHandlers(st, rtr, cch, tk, logger, usage.New(""))
	srv := New(h, logger)
	ln, err := srv.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Shutdown(2 * time.Second) })
	return "http://" + ln.Addr().String(), nil
}

func writeConfigFile(t *testing.T, content string) string {
	t.Helper()
	p := t.TempDir() + "/cfg.json"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// buildConfigWithMocks returns a config JSON pointing at the given mocks.
func buildConfigWithMocks(t *testing.T, mocks map[string]*mock.Server) string {
	t.Helper()
	var tiers []string
	order := []string{"a", "b", "c"}
	for _, name := range order {
		m, ok := mocks[name]
		if !ok {
			continue
		}
		tiers = append(tiers, `{"name":"tier-`+name+`","providers":[{"name":"`+name+`","kind":"openai","base_url":"`+m.URL()+`/v1","api_key_env":"TEST_KEY_`+strings.ToUpper(name)+`","models":["m"]}]}`)
	}
	return `{"listen":"127.0.0.1:0","tiers":[` + strings.Join(tiers, ",") + `],"rtk":{"enabled":true,"min_bytes":500,"max_bytes":10485760},"cache":{"enabled":true,"max_entries":64,"ttl_seconds":3600,"prefix_order":false}}`
}

func chatBody(stream bool, toolContent string) []byte {
	var msgs []any
	msgs = append(msgs, map[string]any{"role": "user", "content": "hello"})
	if toolContent != "" {
		msgs = append(msgs, map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": toolContent},
		}})
	}
	doc := map[string]any{"model": "m", "messages": msgs}
	if stream {
		doc["stream"] = true
	}
	b, _ := json.Marshal(doc)
	return b
}

func post(t *testing.T, url, path string, body []byte) (*http.Response, []byte) {
	t.Helper()
	resp, err := http.Post(url+path, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, data
}

func get(t *testing.T, url, path string) (*http.Response, []byte) {
	t.Helper()
	resp, err := http.Get(url + path)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, data
}

func TestFailoverOrder(t *testing.T) {
	a, _ := mock.New("a")
	defer a.Close()
	b, _ := mock.New("b")
	defer b.Close()
	c, _ := mock.New("c")
	defer c.Close()
	base, _ := testEnv(t, buildConfigWithMocks(t, map[string]*mock.Server{"a": a, "b": b, "c": c}))

	a.SetFail(500)
	b.SetFail(500)

	resp, data := post(t, base, "/v1/chat/completions", chatBody(false, ""))
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200 from c after failover, got %d: %s", resp.StatusCode, data)
	}
	if a.Requests() != 1 || b.Requests() != 1 || c.Requests() != 1 {
		t.Fatalf("expected exactly one request per provider, got a=%d b=%d c=%d", a.Requests(), b.Requests(), c.Requests())
	}
	if got := resp.Header.Get("X-Llrouter-Provider"); got != "c" {
		t.Fatalf("expected provider c to serve, got %q", got)
	}
}

func TestFailoverStopsAtClientError(t *testing.T) {
	a, _ := mock.New("a")
	defer a.Close()
	b, _ := mock.New("b")
	defer b.Close()
	base, _ := testEnv(t, buildConfigWithMocks(t, map[string]*mock.Server{"a": a, "b": b}))

	a.SetFail(400) // client error: must NOT fail over
	resp, _ := post(t, base, "/v1/chat/completions", chatBody(false, ""))
	if resp.StatusCode != 400 {
		t.Fatalf("expected 400 surfaced, got %d", resp.StatusCode)
	}
	if b.Requests() != 0 {
		t.Fatal("client error must not trigger failover")
	}
}

func TestAuthFailover(t *testing.T) {
	a, _ := mock.New("a")
	defer a.Close()
	b, _ := mock.New("b")
	defer b.Close()
	base, _ := testEnv(t, buildConfigWithMocks(t, map[string]*mock.Server{"a": a, "b": b}))

	a.SetFail(401)
	resp, _ := post(t, base, "/v1/chat/completions", chatBody(false, ""))
	if resp.StatusCode != 200 {
		t.Fatalf("expected failover to b, got %d", resp.StatusCode)
	}
	if b.Requests() != 1 {
		t.Fatal("b must serve after 401")
	}
}

func TestAllFailed(t *testing.T) {
	a, _ := mock.New("a")
	defer a.Close()
	base, _ := testEnv(t, buildConfigWithMocks(t, map[string]*mock.Server{"a": a}))

	a.SetFail(503)
	resp, _ := post(t, base, "/v1/chat/completions", chatBody(false, ""))
	if resp.StatusCode != 503 {
		t.Fatalf("expected 503, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Retry-After"); got != "5" {
		t.Fatalf("expected Retry-After: 5, got %q", got)
	}
}

func TestStreamingPassThrough(t *testing.T) {
	a, _ := mock.New("a")
	defer a.Close()
	base, _ := testEnv(t, buildConfigWithMocks(t, map[string]*mock.Server{"a": a}))

	resp, data := post(t, base, "/v1/chat/completions", chatBody(true, ""))
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, data)
	}
	if !strings.Contains(string(data), "from-a") {
		t.Fatalf("stream content missing: %s", data)
	}
	if !strings.Contains(string(data), "[DONE]") {
		t.Fatalf("stream terminator missing: %s", data)
	}
}

func TestStreamingAbortDoesNotFailOver(t *testing.T) {
	a, _ := mock.New("a")
	defer a.Close()
	b, _ := mock.New("b")
	defer b.Close()
	base, _ := testEnv(t, buildConfigWithMocks(t, map[string]*mock.Server{"a": a, "b": b}))

	a.SetAbortMid(true)
	resp, data := post(t, base, "/v1/chat/completions", chatBody(true, ""))
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200 with partial stream, got %d", resp.StatusCode)
	}
	if !strings.Contains(string(data), "from-a") {
		t.Fatalf("expected partial data from a: %s", data)
	}
	if b.Requests() != 0 {
		t.Fatal("mid-stream abort must NOT fail over to b")
	}
}

func TestCacheHit(t *testing.T) {
	a, _ := mock.New("a")
	defer a.Close()
	base, _ := testEnv(t, buildConfigWithMocks(t, map[string]*mock.Server{"a": a}))

	body := chatBody(false, "")
	resp1, data1 := post(t, base, "/v1/chat/completions", body)
	if resp1.Header.Get("X-Llrouter-Cache") != "miss" {
		t.Fatalf("first call must be a miss, got %q", resp1.Header.Get("X-Llrouter-Cache"))
	}
	resp2, data2 := post(t, base, "/v1/chat/completions", body)
	if resp2.Header.Get("X-Llrouter-Cache") != "hit" {
		t.Fatalf("second call must be a hit, got %q", resp2.Header.Get("X-Llrouter-Cache"))
	}
	if !bytes.Equal(data1, data2) {
		t.Fatal("cache hit must return identical body")
	}
	if a.Requests() != 1 {
		t.Fatalf("upstream must be called once, got %d", a.Requests())
	}
}

func TestRTKAppliedOnRelay(t *testing.T) {
	a, _ := mock.New("a")
	defer a.Close()
	base, _ := testEnv(t, buildConfigWithMocks(t, map[string]*mock.Server{"a": a}))

	big := strings.Repeat("repeated tool line\n", 400)
	body := chatBody(false, big)
	resp, _ := post(t, base, "/v1/chat/completions", body)
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	upstream := a.Body()
	if len(upstream) >= len(body) {
		t.Fatalf("upstream must receive compressed body: %d -> %d", len(body), len(upstream))
	}
	if !strings.Contains(string(upstream), "repeated") {
		t.Fatal("compressed body must keep the dedup marker")
	}
}

func TestHealthz(t *testing.T) {
	a, _ := mock.New("a")
	defer a.Close()
	base, _ := testEnv(t, buildConfigWithMocks(t, map[string]*mock.Server{"a": a}))
	resp, data := get(t, base, "/healthz")
	if resp.StatusCode != 200 || !strings.Contains(string(data), "ok") {
		t.Fatalf("healthz: %d %s", resp.StatusCode, data)
	}
}

func TestStatusEndpoint(t *testing.T) {
	a, _ := mock.New("a")
	defer a.Close()
	base, _ := testEnv(t, buildConfigWithMocks(t, map[string]*mock.Server{"a": a}))
	resp, data := get(t, base, "/v1/status")
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var out struct {
		Providers []struct {
			Name string `json:"name"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("status parse: %v", err)
	}
	if len(out.Providers) != 1 || out.Providers[0].Name != "a" {
		t.Fatalf("unexpected providers: %+v", out.Providers)
	}
}

func TestModelsEndpoint(t *testing.T) {
	a, _ := mock.New("a")
	defer a.Close()
	base, _ := testEnv(t, buildConfigWithMocks(t, map[string]*mock.Server{"a": a}))
	resp, data := get(t, base, "/v1/models")
	if resp.StatusCode != 200 || !strings.Contains(string(data), "a/m") {
		t.Fatalf("models: %d %s", resp.StatusCode, data)
	}
}
