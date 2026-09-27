package tests

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestHealthz proves the binary boots, binds, and answers its liveness
// probe — the first thing a supervisor or the agent itself checks.
func TestHealthz(t *testing.T) {
	g := StartGateway(t, GWOpts{Providers: []string{"a"}})
	code, body := g.Get(t, "/healthz")
	if code != 200 {
		t.Fatalf("healthz = %d, want 200 (body %s)", code, body)
	}
	if !strings.Contains(string(body), "ok") {
		t.Errorf("healthz body = %q, want it to contain %q", body, "ok")
	}
}

// TestModelsEndpoint proves model discovery reached the upstream and the
// gateway advertises the union. IDs are provider-namespaced (`a/m`) so
// two providers offering the same model stay distinguishable.
func TestModelsEndpoint(t *testing.T) {
	g := StartGateway(t, GWOpts{
		Providers: []string{"a", "b"},
		Models:    []string{"m", "extra-model"},
	})
	g.Mocks["a"].SetModels([]string{"m", "extra-model"})
	g.Mocks["b"].SetModels([]string{"m"})

	code, body := g.Get(t, "/v1/models")
	if code != 200 {
		t.Fatalf("/v1/models = %d, want 200 (body %s)", code, body)
	}
	var doc struct {
		Data []struct{ ID string } `json:"data"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("decode models: %v (body %s)", err, body)
	}
	seen := map[string]bool{}
	for _, m := range doc.Data {
		seen[m.ID] = true
	}
	for _, want := range []string{"a/m", "a/extra-model", "b/m"} {
		if !seen[want] {
			t.Errorf("model %q missing from /v1/models, got %v", want, seen)
		}
	}
}

// TestChatCompletion is the core path: a real request in, a real provider
// response back out.
func TestChatCompletion(t *testing.T) {
	g := StartGateway(t, GWOpts{Providers: []string{"a"}})
	code, body := g.Post(t, "/v1/chat/completions", ChatBody(false), nil)
	if code != 200 {
		t.Fatalf("chat = %d, want 200 (body %s)", code, body)
	}
	doc := Decode(t, body)
	choices, _ := doc["choices"].([]any)
	if len(choices) == 0 {
		t.Fatalf("no choices in response: %s", body)
	}
	first, _ := choices[0].(map[string]any)
	msg, _ := first["message"].(map[string]any)
	if got, _ := msg["content"].(string); !strings.Contains(got, "mock response from a") {
		t.Errorf("content = %q, want the provider's text", got)
	}
	if g.Mocks["a"].Requests() == 0 {
		t.Error("provider was never called")
	}
}

// TestStreamingChatCompletion proves SSE passthrough end to end: the
// client sees framed events and the terminating [DONE].
func TestStreamingChatCompletion(t *testing.T) {
	g := StartGateway(t, GWOpts{Providers: []string{"a"}})
	code, sse := g.PostStream(t, "/v1/chat/completions", ChatBody(true))
	if code != 200 {
		t.Fatalf("stream = %d, want 200 (body %s)", code, sse)
	}
	if !strings.Contains(sse, "data:") {
		t.Errorf("no SSE frames in stream: %q", sse)
	}
	if !strings.Contains(sse, "[DONE]") {
		t.Errorf("stream did not terminate with [DONE]: %q", sse)
	}
}

// TestMessagesAnthropicClient proves the /v1/messages dialect works for
// Claude Code against a same-kind upstream (the common case: an Anthropic
// model served by an Anthropic provider).
func TestMessagesAnthropicClient(t *testing.T) {
	g := StartGateway(t, GWOpts{Providers: []string{"a"}, Kind: "anthropic"})
	body := map[string]any{
		"model":      "m",
		"max_tokens": 100,
		"messages":   []any{map[string]any{"role": "user", "content": "hello"}},
	}
	code, out := g.Post(t, "/v1/messages", body, nil)
	if code != 200 {
		t.Fatalf("messages = %d, want 200 (body %s)", code, out)
	}
	doc := Decode(t, out)
	if _, ok := doc["content"]; !ok {
		t.Errorf("anthropic response has no content block: %s", out)
	}
}

// TestMessagesCrossKindNonStreamingGap pins a KNOWN GAP: an Anthropic
// client (/v1/messages) routed to an OpenAI provider receives the raw
// OpenAI body. dialect has AnthropicToOpenAIResponse but no
// OpenAIToAnthropicResponse, and the non-streaming matrix in
// pipeline_eval.go translates gemini->* and anthropic->openai only. The
// streaming path DOES translate this direction, which is why the old unit
// test (streaming-only) never caught it.
//
// This test PASSES while the gap is present and FAILS once someone fixes
// it, which is the reminder to delete it.
//
// ponytail: documents the gap, does not fix it — a fix is new product
// behavior and belongs in its own change, not a refactor.
func TestMessagesCrossKindNonStreamingGap(t *testing.T) {
	g := StartGateway(t, GWOpts{Providers: []string{"a"}, Kind: "openai"})
	body := map[string]any{
		"model":      "m",
		"max_tokens": 100,
		"messages":   []any{map[string]any{"role": "user", "content": "hello"}},
	}
	code, out := g.Post(t, "/v1/messages", body, nil)
	if code != 200 {
		t.Fatalf("messages = %d, want 200 (body %s)", code, out)
	}
	doc := Decode(t, out)
	if _, openAIShape := doc["choices"]; !openAIShape {
		t.Errorf("KNOWN GAP FIXED: an anthropic client on /v1/messages no " +
			"longer receives an OpenAI-shaped body. Add OpenAIToAnthropicResponse " +
			"to the non-streaming matrix in pipeline_eval.go, then delete this test.")
	}
}

// TestResponsesEndpoint proves the OpenAI Responses dialect (opencode's
// built-in provider) is served.
func TestResponsesEndpoint(t *testing.T) {
	g := StartGateway(t, GWOpts{Providers: []string{"a"}})
	body := map[string]any{
		"model":    "m",
		"input":    "hello",
		"messages": []any{map[string]any{"role": "user", "content": "hello"}},
	}
	code, out := g.Post(t, "/v1/responses", body, nil)
	if code != 200 {
		t.Fatalf("responses = %d, want 200 (body %s)", code, out)
	}
}

// TestFailoverToSecondProvider is the headline feature: provider a is
// down, the request still succeeds via b, and the response names the
// provider that actually served it.
func TestFailoverToSecondProvider(t *testing.T) {
	g := StartGateway(t, GWOpts{Providers: []string{"a", "b"}})
	g.Mocks["a"].SetFail(500)

	code, body, hdr := g.PostH(t, "/v1/chat/completions", ChatBody(false), nil)
	if code != 200 {
		t.Fatalf("failover = %d, want 200 (body %s)", code, body)
	}
	if !strings.Contains(string(body), "mock response from b") {
		t.Errorf("expected the answer from provider b, got: %s", body)
	}
	if got := hdr.Get("X-Llrouter-Provider"); got != "b" {
		t.Errorf("X-Llrouter-Provider = %q, want %q", got, "b")
	}
	if g.Mocks["a"].Requests() == 0 {
		t.Error("provider a was never tried, so no failover happened")
	}
	if g.Mocks["b"].Requests() == 0 {
		t.Error("provider b was never called")
	}
}

// TestFailoverAllProvidersDown proves exhaustion is reported honestly:
// 503 with per-provider attempts and a Retry-After, not a silent success
// or a hang.
func TestFailoverAllProvidersDown(t *testing.T) {
	g := StartGateway(t, GWOpts{Providers: []string{"a", "b"}})
	g.Mocks["a"].SetFail(500)
	g.Mocks["b"].SetFail(500)

	code, body, hdr := g.PostH(t, "/v1/chat/completions", ChatBody(false), nil)
	if code != 503 {
		t.Fatalf("all-down = %d, want 503 (body %s)", code, body)
	}
	if !strings.Contains(string(body), "attempts") {
		t.Errorf("503 body carries no attempts[] detail: %s", body)
	}
	if got := hdr.Get("Retry-After"); got == "" {
		t.Error("503 exhaustion has no Retry-After header")
	}
}

// TestCacheHit proves a repeated identical request is served from cache
// without touching the provider, that the cache header flips miss->hit,
// and that the served body is byte-identical.
func TestCacheHit(t *testing.T) {
	g := StartGateway(t, GWOpts{Providers: []string{"a"}})

	_, body1, hdr1 := g.PostH(t, "/v1/chat/completions", ChatBody(false), nil)
	if got := hdr1.Get("X-Llrouter-Cache"); got != "miss" {
		t.Fatalf("first request X-Llrouter-Cache = %q, want %q", got, "miss")
	}
	after1 := g.Mocks["a"].Requests()

	_, body2, hdr2 := g.PostH(t, "/v1/chat/completions", ChatBody(false), nil)
	if got := hdr2.Get("X-Llrouter-Cache"); got != "hit" {
		t.Fatalf("second request X-Llrouter-Cache = %q, want %q", got, "hit")
	}
	if string(body1) != string(body2) {
		t.Errorf("cache hit returned a different body:\nfirst:  %s\nsecond: %s", body1, body2)
	}
	after2 := g.Mocks["a"].Requests()
	if after2 != after1 {
		t.Errorf("second identical request hit the provider (%d -> %d); cache did not serve it", after1, after2)
	}
}

// TestStatusEndpoint proves the CLI's data source is populated with the
// connected providers.
func TestStatusEndpoint(t *testing.T) {
	g := StartGateway(t, GWOpts{Providers: []string{"a"}})
	code, body := g.Get(t, "/v1/status")
	if code != 200 {
		t.Fatalf("/v1/status = %d, want 200 (body %s)", code, body)
	}
	var out struct {
		Providers []struct{ Name string } `json:"providers"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("status parse: %v (body %s)", err, body)
	}
	if len(out.Providers) != 1 || out.Providers[0].Name != "a" {
		t.Errorf("unexpected providers: %+v, want exactly [a]", out.Providers)
	}
}

// TestMetricsEndpoint proves Prometheus exposition is served with the
// expected series after a request.
func TestMetricsEndpoint(t *testing.T) {
	g := StartGateway(t, GWOpts{Providers: []string{"a"}})
	g.Post(t, "/v1/chat/completions", ChatBody(false), nil)
	code, body := g.Get(t, "/metrics")
	if code != 200 {
		t.Fatalf("/metrics = %d, want 200", code)
	}
	for _, want := range []string{"routre_requests_total", "routre_uptime_seconds", "routre_cache_misses_total"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("/metrics missing %q:\n%s", want, firstBytes(body, 400))
		}
	}
}

// TestNotFound proves unknown paths get the documented 404 envelope.
func TestNotFound(t *testing.T) {
	g := StartGateway(t, GWOpts{Providers: []string{"a"}})
	code, body := g.Get(t, "/v1/nope")
	if code != 404 {
		t.Fatalf("unknown path = %d, want 404 (body %s)", code, body)
	}
	doc := Decode(t, body)
	if _, ok := doc["error"]; !ok {
		t.Errorf("404 body has no error envelope: %s", body)
	}
}

// TestUIDashboard proves the non-programmer config surface is served.
func TestUIDashboard(t *testing.T) {
	g := StartGateway(t, GWOpts{Providers: []string{"a"}})
	code, body := g.Get(t, "/ui")
	if code != 200 {
		t.Fatalf("/ui = %d, want 200", code)
	}
	if !strings.Contains(string(body), "<html") {
		t.Errorf("/ui did not return an HTML document: %s", firstBytes(body, 200))
	}
}

// TestGatewayAuthRejectsMissingKey proves the optional shared secret
// actually gates the API when configured.
func TestGatewayAuthRejectsMissingKey(t *testing.T) {
	g := StartGateway(t, GWOpts{Providers: []string{"a"}, AuthSecretEnv: "ROUTRE_E2E_SECRET"})

	code, _ := g.Post(t, "/v1/chat/completions", ChatBody(false), nil)
	if code != 401 && code != 403 {
		t.Errorf("unauthenticated request = %d, want 401/403", code)
	}

	code, body := g.Post(t, "/v1/chat/completions", ChatBody(false),
		map[string]string{"X-Routre-Key": "e2e-secret"})
	if code != 200 {
		t.Errorf("authenticated request = %d, want 200 (body %s)", code, body)
	}
}

// TestHealthzExemptFromAuth proves the liveness probe stays open so a
// supervisor does not need the secret.
func TestHealthzExemptFromAuth(t *testing.T) {
	g := StartGateway(t, GWOpts{Providers: []string{"a"}, AuthSecretEnv: "ROUTRE_E2E_SECRET"})
	if code, _ := g.Get(t, "/healthz"); code != 200 {
		t.Errorf("/healthz under auth = %d, want 200 (probes must stay open)", code)
	}
}

// TestUnknownModelForwarded proves forward_unknown keeps a model the
// gateway has never seen working, rather than 404ing.
func TestUnknownModelForwarded(t *testing.T) {
	g := StartGateway(t, GWOpts{Providers: []string{"a"}})
	body := map[string]any{
		"model":    "brand-new-model",
		"messages": []any{map[string]any{"role": "user", "content": "hello"}},
	}
	code, out := g.Post(t, "/v1/chat/completions", body, nil)
	if code != 200 {
		t.Fatalf("forward_unknown = %d, want 200 (body %s)", code, out)
	}
}

// TestStrictModeRejectsUnknownModel proves the opt-in strict mode still
// refuses unknown models — the counterpart to the test above.
func TestStrictModeRejectsUnknownModel(t *testing.T) {
	g := StartGateway(t, GWOpts{
		Providers: []string{"a"},
		Extra:     map[string]any{"forward_unknown": false},
	})
	body := map[string]any{
		"model":    "brand-new-model",
		"messages": []any{map[string]any{"role": "user", "content": "hello"}},
	}
	code, _ := g.Post(t, "/v1/chat/completions", body, nil)
	if code == 200 {
		t.Error("strict mode accepted an unconfigured model; it must refuse")
	}
}

// TestUsageLedger proves token accounting reaches the ledger the CLI
// prints, which is the whole point of the cost feature.
func TestUsageLedger(t *testing.T) {
	g := StartGateway(t, GWOpts{Providers: []string{"a"}})
	g.Post(t, "/v1/chat/completions", ChatBody(false), nil)

	code, body := g.Get(t, "/v1/usage")
	if code != 200 {
		t.Fatalf("/v1/usage = %d, want 200 (body %s)", code, body)
	}
	doc := Decode(t, body)
	rows, _ := doc["rows"].([]any)
	if len(rows) == 0 {
		t.Errorf("usage ledger is empty after a completed request: %s", body)
	}
}

// TestCLIListAgainstLiveGateway proves the `list` command reads the
// running gateway — the CLI/GUI contract.
func TestCLIListAgainstLiveGateway(t *testing.T) {
	g := StartGateway(t, GWOpts{Providers: []string{"a"}})
	g.Post(t, "/v1/chat/completions", ChatBody(false), nil)

	out, err := g.Run(t, "list", "-url", g.Base)
	if err != nil {
		t.Fatalf("routre list failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "m") {
		t.Errorf("`list` output does not mention the connected model:\n%s", out)
	}
}

// TestCLICheckReportsProviders proves `check` validates config + keys
// against the same config the gateway loaded.
func TestCLICheckReportsProviders(t *testing.T) {
	g := StartGateway(t, GWOpts{Providers: []string{"a", "b"}})
	out, err := g.Run(t, "check")
	if err != nil {
		t.Fatalf("routre check failed: %v\n%s", err, out)
	}
	for _, want := range []string{"tier e2e", "provider a", "provider b"} {
		if !strings.Contains(out, want) {
			t.Errorf("`check` output missing %q:\n%s", want, out)
		}
	}
}

// TestCLIVersion proves the binary reports a version string.
func TestCLIVersion(t *testing.T) {
	g := StartGateway(t, GWOpts{Providers: []string{"a"}})
	out, _ := g.Run(t, "version")
	if !strings.Contains(out, "routre") {
		t.Errorf("`version` output unexpected: %q", out)
	}
}

// TestUnknownSubcommandFails proves bad input is rejected, not silently
// swallowed.
func TestUnknownSubcommandFails(t *testing.T) {
	g := StartGateway(t, GWOpts{Providers: []string{"a"}})
	if _, err := g.Run(t, "not-a-command"); err == nil {
		t.Error("unknown subcommand exited 0; it must fail")
	}
}

func firstBytes(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n]) + "..."
	}
	return string(b)
}
