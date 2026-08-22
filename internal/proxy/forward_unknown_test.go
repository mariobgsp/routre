package proxy

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"routre-cli/internal/mock"
)

// cfgFor builds a gateway config pointing at the given mocks with an
// explicit forward_unknown value (the flag under test here).
func cfgFor(t *testing.T, forwardUnknown bool, mocks ...*mock.Server) string {
	t.Helper()
	keys := []string{"TEST_KEY_A", "TEST_KEY_B", "TEST_KEY_C"}
	var provs []string
	for i, m := range mocks {
		provs = append(provs, `{"name":"`+m.Name+`","kind":"openai","base_url":"`+m.URL()+`/v1","api_key_env":"`+keys[i]+`","models":["m"]}`)
	}
	return `{"listen":"127.0.0.1:0",` +
		`"tiers":[{"name":"t1","providers":[` + strings.Join(provs, ",") + `]}],` +
		`"rtk":{"enabled":true,"min_bytes":500,"max_bytes":10485760},` +
		`"cache":{"enabled":true,"max_entries":64,"ttl_seconds":3600,"prefix_order":false},` +
		`"forward_unknown":` + boolJSON(forwardUnknown) + `}`
}

func boolJSON(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func chatBodyFor(model string, stream bool) []byte {
	doc := map[string]any{
		"model":    model,
		"messages": []any{map[string]any{"role": "user", "content": "hello"}},
	}
	if stream {
		doc["stream"] = true
	}
	b, _ := json.Marshal(doc)
	return b
}

// A wildcard-forwarded model rejected by the first provider (404 = "this
// provider lacks it") must fail over to the next provider instead of
// surfacing the first rejection.
func TestWildcardModelCascadesPastClientError(t *testing.T) {
	a, _ := mock.New("a")
	defer a.Close()
	a.SetFail(404) // "model not found" from this provider
	b, _ := mock.New("b")
	defer b.Close()

	base, _ := testEnv(t, cfgFor(t, true, a, b))

	resp, data := post(t, base, "/v1/chat/completions", chatBodyFor("future-x", false))
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200 from failover to b, got %d: %s", resp.StatusCode, data)
	}
	if !strings.Contains(string(data), "mock-b") {
		t.Fatalf("expected the response to come from provider b: %s", data)
	}
	if a.Requests() != 1 || b.Requests() != 1 {
		t.Fatalf("expected exactly one attempt per provider, a=%d b=%d", a.Requests(), b.Requests())
	}
}

// Strict mode (forward_unknown=false) keeps the original contract: an
// unlisted model returns model_not_found without ever reaching upstream.
func TestStrictModeRejectsUnknownModel(t *testing.T) {
	a, _ := mock.New("a")
	defer a.Close()

	cfg := strings.Replace(cfgFor(t, false, a), `"forward_unknown":false`,
		`"forward_unknown":false,"fallbacks":[]`, 1)
	base, _ := testEnv(t, cfg)

	resp, data := post(t, base, "/v1/chat/completions", chatBodyFor("future-x", false))
	if resp.StatusCode != 503 {
		t.Fatalf("expected 503 model_not_found, got %d: %s", resp.StatusCode, data)
	}
	if !strings.Contains(string(data), `"model_not_found"`) {
		t.Fatalf("expected model_not_found error type: %s", data)
	}
	if a.Requests() != 0 {
		t.Fatalf("strict mode must not contact providers, got %d requests", a.Requests())
	}
}

// The streaming path must carry provider-reported prompt-cache reads all
// the way into the usage ledger (`routre-cli list` reads this endpoint).
func TestStreamingCacheReadReachesLedger(t *testing.T) {
	a, _ := mock.New("a")
	defer a.Close()
	a.SetStream(true)

	cfg := cfgFor(t, false, a) // whitelisted model: strictness irrelevant
	if !json.Valid([]byte(cfg)) {
		t.Fatalf("invalid generated config: %q", cfg)
	}
	base, _ := testEnv(t, cfg)

	resp, body := post(t, base, "/v1/chat/completions", chatBodyFor("m", true))
	if resp.StatusCode != 200 {
		t.Fatalf("streaming request failed: %d: %s", resp.StatusCode, body)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()

	_, usageData := get(t, base, "/v1/usage")
	var doc struct {
		Rows []struct {
			Provider        string `json:"provider"`
			CacheReadTokens int64  `json:"cache_read_tokens"`
			PromptTokens    int64  `json:"prompt_tokens"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(usageData, &doc); err != nil {
		t.Fatalf("usage parse: %v\n%s", err, usageData)
	}
	for _, r := range doc.Rows {
		if r.CacheReadTokens != 0 || r.PromptTokens != 0 {
			if r.CacheReadTokens != 6 || r.PromptTokens != 12 {
				t.Fatalf("unexpected ledger row: %+v (raw: %s)", r, usageData)
			}
			return // streaming cached_tokens reached the ledger
		}
	}
	t.Fatalf("no ledger row with cache_read_tokens: %s", usageData)
}
