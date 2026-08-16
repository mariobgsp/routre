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

// routerStatus fetches /v1/status and returns the provider snapshot.
func routerStatus(t *testing.T, base string) []router.Status {
	t.Helper()
	_, data := get(t, base, "/v1/status")
	var out struct {
		Providers []router.Status `json:"providers"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("status parse: %v", err)
	}
	return out.Providers
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
	// Transient 5xx are retried once per candidate before failover, so a
	// and b each see 2 attempts (original + 1 retry); c serves first try.
	if a.Requests() != 2 || b.Requests() != 2 || c.Requests() != 1 {
		t.Fatalf("expected retry-then-failover counts a=2 b=2 c=1, got a=%d b=%d c=%d", a.Requests(), b.Requests(), c.Requests())
	}
	if got := resp.Header.Get("X-Llrouter-Provider"); got != "c" {
		t.Fatalf("expected provider c to serve, got %q", got)
	}
}

// TestFreeVariantFailover: the user's default provider (paid model) fails;
// the request must fall through to a provider serving a FREE variant of the
// same model, with the upstream body rewritten to the free model name.
func TestFreeVariantFailover(t *testing.T) {
	paid, _ := mock.New("paid")
	defer paid.Close()
	free, _ := mock.New("free")
	defer free.Close()

	cfgJSON := `{"listen":"127.0.0.1:0","tiers":[
		{"name":"subscription","providers":[{"name":"paid","kind":"openai","base_url":"` + paid.URL() + `/v1","api_key_env":"TEST_KEY_A","models":["m"]}]},
		{"name":"free","providers":[{"name":"free","kind":"openai","base_url":"` + free.URL() + `/v1","api_key_env":"TEST_KEY_B","models":["m-free"]}]}
	]}`
	base, _ := testEnv(t, cfgJSON)

	paid.SetFail(500)

	body := chatBody(false, "")
	resp, data := post(t, base, "/v1/chat/completions", body)
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200 from free provider after paid 500, got %d: %s", resp.StatusCode, data)
	}
	if got := resp.Header.Get("X-Llrouter-Provider"); got != "free" {
		t.Fatalf("expected provider free to serve, got %q", got)
	}
	if got := resp.Header.Get("X-Llrouter-Free"); got != "m-free" {
		t.Fatalf("expected X-Llrouter-Free: m-free, got %q", got)
	}
	// The upstream must have received the rewritten model name.
	var sent struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(free.Body(), &sent); err != nil || sent.Model != "m-free" {
		t.Fatalf("upstream must receive model m-free, got %q (err %v)", sent.Model, err)
	}
}

// TestCreditsFailover: a 402 (or 401 CreditsError) from the preferred
// provider must fail over to the next candidate instead of surfacing.
func TestCreditsFailover(t *testing.T) {
	cred, _ := mock.New("cred")
	defer cred.Close()
	ok, _ := mock.New("ok")
	defer ok.Close()

	cfgJSON := `{"listen":"127.0.0.1:0","tiers":[
		{"name":"subscription","providers":[{"name":"cred","kind":"openai","base_url":"` + cred.URL() + `/v1","api_key_env":"TEST_KEY_A","models":["m"]}]},
		{"name":"cheap","providers":[{"name":"ok","kind":"openai","base_url":"` + ok.URL() + `/v1","api_key_env":"TEST_KEY_B","models":["m"]}]}
	]}`
	base, _ := testEnv(t, cfgJSON)

	cred.SetFail(402)

	resp, _ := post(t, base, "/v1/chat/completions", chatBody(false, ""))
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200 from ok after 402 failover, got %d", resp.StatusCode)
	}
	if ok.Requests() != 1 {
		t.Fatal("ok must serve after 402")
	}
	// Credits failures must not cooldown the provider: a second request
	// still tries it first (use a different body to bypass the cache).
	cred.SetFail(0)
	body2 := chatBody(false, "")
	body2 = bytes.Replace(body2, []byte("\"content\":\"hello\""), []byte("\"content\":\"hello again\""), 1)
	resp, _ = post(t, base, "/v1/chat/completions", body2)
	if got := resp.Header.Get("X-Llrouter-Provider"); got != "cred" {
		t.Fatalf("cred must recover immediately (no cooldown), got %q", got)
	}
}

// TestCreditsErrorBodyFailover: opencode-zen style 401 with a CreditsError
// body must be treated as credits (fail over, no cooldown), while a plain
// 401 stays auth.
func TestCreditsErrorBodyFailover(t *testing.T) {
	cred, _ := mock.New("cred")
	defer cred.Close()
	ok, _ := mock.New("ok")
	defer ok.Close()

	cfgJSON := `{"listen":"127.0.0.1:0","tiers":[
		{"name":"subscription","providers":[{"name":"cred","kind":"openai","base_url":"` + cred.URL() + `/v1","api_key_env":"TEST_KEY_A","models":["m"]}]},
		{"name":"cheap","providers":[{"name":"ok","kind":"openai","base_url":"` + ok.URL() + `/v1","api_key_env":"TEST_KEY_B","models":["m"]}]}
	]}`
	base, _ := testEnv(t, cfgJSON)

	cred.SetFail(401)
	cred.SetFailBody(`{"type":"error","error":{"type":"CreditsError","message":"Insufficient balance"}}`)

	resp, _ := post(t, base, "/v1/chat/completions", chatBody(false, ""))
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200 from ok after credits-401 failover, got %d", resp.StatusCode)
	}
	if ok.Requests() != 1 {
		t.Fatal("ok must serve after credits-401")
	}

	// Same 401 but WITHOUT a credits body stays auth — and auth failures
	// DO escalate cooldown (unlike credits). Verify the distinction at the
	// classification level.
	if got := router.ClassifyStatusBody(401, []byte(`{"type":"error","error":{"type":"CreditsError","message":"Insufficient balance"}}`)); got != router.ErrCredits {
		t.Fatalf("credits-401 must classify as ErrCredits, got %v", got)
	}
	if got := router.ClassifyStatusBody(401, []byte(`{"error":{"message":"invalid key"}}`)); got != router.ErrAuth {
		t.Fatalf("plain 401 must classify as ErrAuth, got %v", got)
	}
}

// TestFallbackModelChain: when the requested model's own providers all
// fail, the request must fall through to the configured fallback models
// (free tiers) with the upstream body rewritten to the fallback model.
func TestFallbackModelChain(t *testing.T) {
	pref, _ := mock.New("pref")
	defer pref.Close()
	freeSrv, _ := mock.New("free")
	defer freeSrv.Close()

	cfgJSON := `{"listen":"127.0.0.1:0","fallbacks":["free-model"],"tiers":[
		{"name":"subscription","providers":[{"name":"pref","kind":"openai","base_url":"` + pref.URL() + `/v1","api_key_env":"TEST_KEY_A","models":["m"]}]},
		{"name":"free","providers":[{"name":"free","kind":"openai","base_url":"` + freeSrv.URL() + `/v1","api_key_env":"TEST_KEY_B","models":["free-model"]}]}
	]}`
	base, _ := testEnv(t, cfgJSON)

	pref.SetFail(500)

	body := chatBody(false, "")
	resp, data := post(t, base, "/v1/chat/completions", body)
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200 from fallback provider, got %d: %s", resp.StatusCode, data)
	}
	if got := resp.Header.Get("X-Llrouter-Provider"); got != "free" {
		t.Fatalf("expected provider free to serve, got %q", got)
	}
	// The upstream must have received the FALLBACK model, not the
	// requested one — this is the exact bug the live test caught (the
	// request reached OpenRouter with the paid model name verbatim).
	var sent struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(freeSrv.Body(), &sent); err != nil || sent.Model != "free-model" {
		t.Fatalf("upstream must receive model free-model, got %q (err %v)", sent.Model, err)
	}
}

// TestClampMaxTokens: a fallback provider with a max_tokens ceiling must
// receive the request with max_tokens capped, not the original (a request
// sized for deepseek-v4-flash's 384k would be rejected by OpenRouter free
// models).
func TestClampMaxTokens(t *testing.T) {
	pref, _ := mock.New("pref")
	defer pref.Close()
	freeSrv, _ := mock.New("free")
	defer freeSrv.Close()

	cfgJSON := `{"listen":"127.0.0.1:0","fallbacks":["free-model"],"tiers":[
		{"name":"subscription","providers":[{"name":"pref","kind":"openai","base_url":"` + pref.URL() + `/v1","api_key_env":"TEST_KEY_A","models":["m"]}]},
		{"name":"free","providers":[{"name":"free","kind":"openai","base_url":"` + freeSrv.URL() + `/v1","api_key_env":"TEST_KEY_B","models":["free-model"],"max_tokens":131072}]}
	]}`
	base, _ := testEnv(t, cfgJSON)

	pref.SetFail(500)

	// Simulate a subagent asking for the full 384k ceiling.
	var doc map[string]any
	_ = json.Unmarshal(chatBody(false, ""), &doc)
	doc["max_tokens"] = 384000
	body, _ := json.Marshal(doc)
	resp, data := post(t, base, "/v1/chat/completions", body)
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200 from fallback with clamped max_tokens, got %d: %s", resp.StatusCode, data)
	}
	var sent struct {
		Model     string `json:"model"`
		MaxTokens int64  `json:"max_tokens"`
	}
	if err := json.Unmarshal(freeSrv.Body(), &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Model != "free-model" {
		t.Fatalf("expected model free-model, got %q", sent.Model)
	}
	if sent.MaxTokens >= 131072 || sent.MaxTokens < 100000 {
		t.Fatalf("expected max_tokens clamped below 131072 (prompt-aware), got %d", sent.MaxTokens)
	}
}

// TestMetricsEndpoint: /metrics must render Prometheus text with counters.
func TestMetricsEndpoint(t *testing.T) {
	a, _ := mock.New("a")
	defer a.Close()
	base, _ := testEnv(t, buildConfigWithMocks(t, map[string]*mock.Server{"a": a}))

	post(t, base, "/v1/chat/completions", chatBody(false, ""))

	resp, data := get(t, base, "/metrics")
	if resp.StatusCode != 200 {
		t.Fatalf("metrics: %d", resp.StatusCode)
	}
	s := string(data)
	for _, want := range []string{"routre_requests_total", "routre_uptime_seconds", "routre_cache_misses_total"} {
		if !strings.Contains(s, want) {
			t.Fatalf("metrics output missing %q:\n%s", want, s)
		}
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

// TestStreamingUpstream500FailsOver: a streaming request whose upstream
// answers 500 must fail over to the next provider and report the failure
// (cooldown escalation), not stream the error body or reset cooldown.
func TestStreamingUpstream500FailsOver(t *testing.T) {
	a, _ := mock.New("a")
	defer a.Close()
	b, _ := mock.New("b")
	defer b.Close()
	base, _ := testEnv(t, buildConfigWithMocks(t, map[string]*mock.Server{"a": a, "b": b}))

	a.SetFail(500)
	resp, data := post(t, base, "/v1/chat/completions", chatBody(true, ""))
	if resp.StatusCode != 200 {
		t.Fatalf("expected failover to b (200), got %d: %s", resp.StatusCode, data)
	}
	if !strings.Contains(string(data), "from-b") {
		t.Fatalf("expected stream from b after a failed: %s", data)
	}
	if b.Requests() == 0 {
		t.Fatal("b must receive the request after a 500s")
	}
	// a must be cooling down (failure reported, not reset by a success).
	st := routerStatus(t, base)
	for _, p := range st {
		if p.Provider == "a" && p.CooldownRemaining <= 0 {
			t.Fatalf("provider a must be in cooldown after streaming 500, got %+v", p)
		}
	}
}

// TestStreamingUpstream429Surfaces: when every provider 429s on a
// streaming request, the client must get a clean 503 all_providers_failed
// (not a raw error stream) and providers must be cooling down.
func TestStreamingUpstream429Surfaces(t *testing.T) {
	a, _ := mock.New("a")
	defer a.Close()
	b, _ := mock.New("b")
	defer b.Close()
	base, _ := testEnv(t, buildConfigWithMocks(t, map[string]*mock.Server{"a": a, "b": b}))

	a.SetFail(429)
	b.SetFail(429)
	resp, data := post(t, base, "/v1/chat/completions", chatBody(true, ""))
	if resp.StatusCode != 503 {
		t.Fatalf("expected 503 all_providers_failed, got %d: %s", resp.StatusCode, data)
	}
	if !strings.Contains(string(data), "all_providers_failed") && !strings.Contains(string(data), "all providers") {
		t.Fatalf("expected all-failed error body, got: %s", data)
	}
}

// TestModelNotConfiguredVsCooldown: a model no provider lists must 503
// with model_not_found; a model every serving provider is cooling down on
// must 503 with providers_unavailable + Retry-After (never the misleading
// "no configured provider serves model").
func TestModelNotConfiguredVsCooldown(t *testing.T) {
	a, _ := mock.New("a")
	defer a.Close()
	base, _ := testEnv(t, buildConfigWithMocks(t, map[string]*mock.Server{"a": a}))

	// Model absent from every provider list.
	body := bytes.Replace(chatBody(false, ""), []byte(`"m"`), []byte(`"no-such-model"`), 1)
	resp, data := post(t, base, "/v1/chat/completions", body)
	if resp.StatusCode != 503 {
		t.Fatalf("expected 503, got %d", resp.StatusCode)
	}
	if !strings.Contains(string(data), "model_not_found") {
		t.Fatalf("expected model_not_found, got: %s", data)
	}

	// Provider serves the model but is in cooldown (from the 503 above).
	// Force a cooldown via two quick 500s, then check the distinction.
	a.SetFail(500)
	_, _ = post(t, base, "/v1/chat/completions", chatBody(false, ""))
	_, _ = post(t, base, "/v1/chat/completions", chatBody(false, ""))
	resp, data = post(t, base, "/v1/chat/completions", chatBody(false, ""))
	if resp.StatusCode != 503 {
		t.Fatalf("expected 503, got %d", resp.StatusCode)
	}
	if !strings.Contains(string(data), "providers_unavailable") {
		t.Fatalf("expected providers_unavailable (not model_not_found), got: %s", data)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Fatal("expected Retry-After on providers_unavailable")
	}
}

// TestStreamingAcceptHeader: the streaming relay must send an explicit
// Accept: text/event-stream to the upstream when the client sent none.
func TestStreamingAcceptHeader(t *testing.T) {
	a, _ := mock.New("a")
	defer a.Close()
	base, _ := testEnv(t, buildConfigWithMocks(t, map[string]*mock.Server{"a": a}))

	_, _ = post(t, base, "/v1/chat/completions", chatBody(true, ""))
	if got := a.Header().Get("Accept"); got != "text/event-stream" {
		t.Fatalf("upstream Accept = %q, want text/event-stream", got)
	}
}

// TestStreamingBetaPassthrough: beta headers the client sent must reach
// the upstream on the streaming path (parity with the non-streaming
// relay).
func TestStreamingBetaPassthrough(t *testing.T) {
	a, _ := mock.New("a")
	defer a.Close()
	base, _ := testEnv(t, buildConfigWithMocks(t, map[string]*mock.Server{"a": a}))

	req, _ := http.NewRequest(http.MethodPost, base+"/v1/chat/completions", bytes.NewReader(chatBody(true, "")))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Anthropic-Beta", "prompt-caching-2024-07-31")
	req.Header.Set("OpenAI-Beta", "responses")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if got := a.Header().Get("Anthropic-Beta"); got != "prompt-caching-2024-07-31" {
		t.Fatalf("Anthropic-Beta upstream = %q", got)
	}
	if got := a.Header().Get("OpenAI-Beta"); got != "responses" {
		t.Fatalf("OpenAI-Beta upstream = %q", got)
	}
}

// TestIsStreamingFalsePositive: a literal "stream":true inside user
// content must NOT switch a request into streaming mode.
func TestIsStreamingFalsePositive(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"the flag is \"stream\":true here"}]}`)
	if isStreaming(body) {
		t.Fatal("literal \"stream\":true in content must not mark the request streaming")
	}
	if !isStreaming([]byte(`{"model":"m","stream":true,"messages":[]}`)) {
		t.Fatal("top-level stream:true must be detected")
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

// TestStreamingSuccessClassifiedOK: a successful stream must be counted
// as class ok (not client_error — a regression where relayStream's
// status-0 success return fell through to the error classification).
func TestStreamingSuccessClassifiedOK(t *testing.T) {
	a, _ := mock.New("a")
	defer a.Close()
	base, _ := testEnv(t, buildConfigWithMocks(t, map[string]*mock.Server{"a": a}))

	post(t, base, "/v1/chat/completions", chatBody(true, ""))

	resp, data := get(t, base, "/metrics")
	if resp.StatusCode != 200 {
		t.Fatalf("metrics: %d", resp.StatusCode)
	}
	s := string(data)
	if !strings.Contains(s, `routre_requests_total{client="go-http-client/1.1",provider="a",model="m",class="ok"}`) {
		t.Fatalf("streaming success must be classified ok; metrics:\n%s", s)
	}
	if strings.Contains(s, `class="client_error"`) {
		t.Fatalf("streaming success must NOT be client_error; metrics:\n%s", s)
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

	// The cache hit must credit the upstream-reported prompt count (the
	// mock reports 10) as saved tokens, not the gateway's estimate.
	_, usageData := get(t, base, "/v1/usage")
	var out struct {
		Rows []usage.Row `json:"rows"`
	}
	if err := json.Unmarshal(usageData, &out); err != nil {
		t.Fatalf("usage parse: %v", err)
	}
	var found *usage.Row
	for i, row := range out.Rows {
		if row.CacheSavedTokens > 0 {
			found = &out.Rows[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected a usage row with cache-saved tokens")
	}
	if found.CacheSavedTokens != 10 {
		t.Fatalf("cache hit must credit upstream-reported prompt tokens (10), got %d", found.CacheSavedTokens)
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

// TestNonStreamingSetsContentType: the non-streaming relay must send
// Content-Type: application/json upstream. Some upstreams (opencode.ai)
// return 500 without it, which previously made every non-streaming
// request fail over and poison cooldowns, surfacing as misleading
// model_not_found 503s.
func TestNonStreamingSetsContentType(t *testing.T) {
	a, _ := mock.New("a")
	defer a.Close()
	base, _ := testEnv(t, buildConfigWithMocks(t, map[string]*mock.Server{"a": a}))

	resp, _ := post(t, base, "/v1/chat/completions", chatBody(false, ""))
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	ct := a.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("upstream Content-Type = %q, want application/json", ct)
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
