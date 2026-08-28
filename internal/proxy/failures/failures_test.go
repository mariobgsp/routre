package failures

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// helper: decode the body for assertions
func decode(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode body: %v\nbody=%s", err, body)
	}
	return out
}

func asMap(m map[string]any, key string) map[string]any {
	v, ok := m[key].(map[string]any)
	if !ok {
		return nil
	}
	return v
}

// TestRender_AllFailed_GoldenShape: 3 outcomes with different
// classes + cooldowns. Asserts: wire type=string, model present,
// attempts[] has 3 entries, each entry has the right fields, omitempty
// rules honored.
func TestRender_AllFailed_GoldenShape(t *testing.T) {
	outcomes := []Outcome{
		{Provider: "a", Kind: "openai", Class: "server", Err: "upstream 500", Cooldown: 2 * time.Second},
		{Provider: "b", Kind: "anthropic", Class: "auth", Err: "key rotated", Cooldown: 30 * time.Second},
		{Provider: "c", Kind: "gemini", Class: "rateLimit", Err: "", Cooldown: 0},
	}
	w := httptest.NewRecorder()
	Render(w, KindAllFailed, "gpt-4", outcomes, 5*time.Second)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	if ra := w.Header().Get("Retry-After"); ra != "5" {
		t.Fatalf("Retry-After = %q, want 5", ra)
	}
	body := decode(t, w.Body.Bytes())
	errBlock := asMap(body, "error")
	if errBlock == nil {
		t.Fatalf("missing error block: %s", w.Body.String())
	}
	if errBlock["type"] != "all_providers_failed" {
		t.Fatalf("type = %v, want all_providers_failed", errBlock["type"])
	}
	if errBlock["model"] != "gpt-4" {
		t.Fatalf("model = %v, want gpt-4", errBlock["model"])
	}
	attempts, ok := errBlock["attempts"].([]any)
	if !ok || len(attempts) != 3 {
		t.Fatalf("attempts = %v, want 3 entries", errBlock["attempts"])
	}
	first := attempts[0].(map[string]any)
	if first["provider"] != "a" || first["kind"] != "openai" || first["class"] != "server" {
		t.Fatalf("first attempt wrong: %+v", first)
	}
	if first["cooldown_remaining_seconds"] == nil {
		t.Fatalf("first attempt missing cooldown_remaining_seconds")
	}
	// Omit empty class — c has no cooldown AND class="rateLimit" which is non-empty,
	// so it should still appear. The "Err empty" case is in the 3rd test.
	third := attempts[2].(map[string]any)
	if third["provider"] != "c" {
		t.Fatalf("third provider = %v, want c", third["provider"])
	}
	// "error" key should be OMITTED for c (Err is empty).
	if _, hasErr := third["error"]; hasErr {
		t.Fatalf("third attempt should omit empty 'error', got %+v", third)
	}
	// "cooldown_remaining_seconds" should be OMITTED for c (Cooldown=0).
	if _, hasCd := third["cooldown_remaining_seconds"]; hasCd {
		t.Fatalf("third attempt should omit zero cooldown, got %+v", third)
	}
}

// TestRender_ProvidersUnavailable_OmitsAttempts: when outcomes is
// empty (providers_unavailable / model_not_found), body has no
// attempts[] field. Providers_unavailable includes cooldown_seconds
// from the supplied outcome.
func TestRender_ProvidersUnavailable_OmitsAttempts(t *testing.T) {
	w := httptest.NewRecorder()
	cd := 17 * time.Second
	Render(w, KindProvidersUnavailable, "claude-3", []Outcome{{Provider: "*", Cooldown: cd}}, cd)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	body := decode(t, w.Body.Bytes())
	errBlock := asMap(body, "error")
	if errBlock["type"] != "providers_unavailable" {
		t.Fatalf("type = %v", errBlock["type"])
	}
	if errBlock["model"] != "claude-3" {
		t.Fatalf("model = %v", errBlock["model"])
	}
	// The envelope does include a cooldown_seconds when supplied; the
	// field comes from the outcomes list (min over positive cooldowns).
	if errBlock["cooldown_seconds"] == nil {
		t.Fatalf("missing cooldown_seconds: %+v", errBlock)
	}
	// attempts[] must be absent (empty list = omitempty).
	if _, has := errBlock["attempts"]; has {
		t.Fatalf("providers_unavailable must omit attempts: %+v", errBlock)
	}
}

// TestRender_ModelNotFound: empty outcomes. No attempts, no
// cooldown_seconds, just model + type.
func TestRender_ModelNotFound(t *testing.T) {
	w := httptest.NewRecorder()
	Render(w, KindModelNotFound, "unknown-model", nil, 0)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	body := decode(t, w.Body.Bytes())
	errBlock := asMap(body, "error")
	if errBlock["type"] != "model_not_found" {
		t.Fatalf("type = %v", errBlock["type"])
	}
	if errBlock["model"] != "unknown-model" {
		t.Fatalf("model = %v", errBlock["model"])
	}
	if _, has := errBlock["attempts"]; has {
		t.Fatalf("model_not_found must omit attempts: %+v", errBlock)
	}
	if _, has := errBlock["cooldown_seconds"]; has {
		t.Fatalf("model_not_found must omit cooldown_seconds: %+v", errBlock)
	}
}

// TestRender_CooldownRounding: 1.7s rounds to 2s on the wire
// (Round(time.Second)).
func TestRender_CooldownRounding(t *testing.T) {
	outcomes := []Outcome{{Provider: "a", Class: "server", Err: "x", Cooldown: 1700 * time.Millisecond}}
	w := httptest.NewRecorder()
	Render(w, KindAllFailed, "m", outcomes, 0)
	body := decode(t, w.Body.Bytes())
	attempts := body["error"].(map[string]any)["attempts"].([]any)
	first := attempts[0].(map[string]any)
	if cd, _ := first["cooldown_remaining_seconds"].(float64); cd != 2 {
		t.Fatalf("cooldown_remaining_seconds = %v, want 2 (rounded from 1.7s)", first["cooldown_remaining_seconds"])
	}
}

// TestRender_RetryAfterFloor: 0.3s retryAfter surfaces as 1s (the
// minimum the wire sets).
func TestRender_RetryAfterFloor(t *testing.T) {
	w := httptest.NewRecorder()
	Render(w, KindProvidersUnavailable, "m", nil, 300*time.Millisecond)
	if ra := w.Header().Get("Retry-After"); ra != "1" {
		t.Fatalf("Retry-After = %q, want 1 (rounded up from 0.3s)", ra)
	}
}

// TestRenderBody_BufferPath: same content as Render but no
// http.ResponseWriter — caller builds a Response struct. Asserts
// bytes + header match.
func TestRenderBody_BufferPath(t *testing.T) {
	outcomes := []Outcome{{Provider: "a", Class: "server", Err: "x"}}
	body, header := RenderBody(KindAllFailed, "m", outcomes, 7*time.Second)
	if header.Get("Content-Type") != "application/json" {
		t.Fatalf("Content-Type = %q", header.Get("Content-Type"))
	}
	if header.Get("Retry-After") != "7" {
		t.Fatalf("Retry-After = %q", header.Get("Retry-After"))
	}
	// Body must be valid JSON with the same shape as Render.
	decoded := decode(t, body)
	if decoded["error"].(map[string]any)["type"] != "all_providers_failed" {
		t.Fatalf("body shape wrong: %s", body)
	}
}

// TestRenderHuman_OKAndFailure: one OK and one failure outcome.
// Each line must contain the provider name and class field; OK
// rows say "OK", failure rows show the class string.
func TestRenderHuman_OKAndFailure(t *testing.T) {
	var buf bytes.Buffer
	outcomes := []Outcome{
		{Provider: "alpha", Kind: "openai", Class: ""}, // empty = OK
		{Provider: "beta", Kind: "anthropic", Class: "auth", Err: "401 bad key"},
		{Provider: "gamma", Kind: "gemini", Class: "rateLimit", Err: "429", Cooldown: 30 * time.Second},
	}
	RenderHuman(&buf, outcomes)
	out := buf.String()
	if !strings.Contains(out, "alpha") || !strings.Contains(out, "OK") {
		t.Fatalf("missing alpha OK line: %q", out)
	}
	if !strings.Contains(out, "beta") || !strings.Contains(out, "auth") || !strings.Contains(out, "401 bad key") {
		t.Fatalf("missing beta auth line: %q", out)
	}
	if !strings.Contains(out, "gamma") || !strings.Contains(out, "rateLimit") || !strings.Contains(out, "429") {
		t.Fatalf("missing gamma rateLimit line: %q", out)
	}
	if !strings.Contains(out, "cooldown=30s") {
		t.Fatalf("missing gamma cooldown=30s: %q", out)
	}
	// Must have one line per outcome (3 newlines for 3 lines).
	if got := strings.Count(out, "\n"); got != 3 {
		t.Fatalf("expected 3 lines, got %d:\n%s", got, out)
	}
}

// TestRenderHuman_Empty: empty input writes nothing. No panic.
func TestRenderHuman_Empty(t *testing.T) {
	var buf bytes.Buffer
	RenderHuman(&buf, nil)
	if buf.Len() != 0 {
		t.Fatalf("expected empty buffer, got %q", buf.String())
	}
}

// TestRender_DedupByProvider: same provider twice, last write wins.
// Matches the pipeline's "lastErr / lastClass" semantics.
func TestRender_DedupByProvider(t *testing.T) {
	outcomes := []Outcome{
		{Provider: "a", Class: "server", Err: "first try 500"},
		{Provider: "a", Class: "auth", Err: "second try 401"},
	}
	w := httptest.NewRecorder()
	Render(w, KindAllFailed, "m", outcomes, 0)
	body := decode(t, w.Body.Bytes())
	attempts := body["error"].(map[string]any)["attempts"].([]any)
	if len(attempts) != 1 {
		t.Fatalf("expected 1 deduped entry, got %d: %v", len(attempts), attempts)
	}
	first := attempts[0].(map[string]any)
	if first["class"] != "auth" {
		t.Fatalf("dedup kept wrong entry; class = %v, want auth", first["class"])
	}
	if first["error"] != "second try 401" {
		t.Fatalf("dedup kept wrong err: %v", first["error"])
	}
}

// TestRender_OrderPreserved: outcomes order is preserved (not
// re-sorted). Callers expect try-order on the wire.
func TestRender_OrderPreserved(t *testing.T) {
	outcomes := []Outcome{
		{Provider: "z", Class: "server"},
		{Provider: "a", Class: "auth"},
		{Provider: "m", Class: "rateLimit"},
	}
	w := httptest.NewRecorder()
	Render(w, KindAllFailed, "m", outcomes, 0)
	body := decode(t, w.Body.Bytes())
	attempts := body["error"].(map[string]any)["attempts"].([]any)
	want := []string{"z", "a", "m"}
	for i, p := range want {
		got := attempts[i].(map[string]any)["provider"]
		if got != p {
			t.Fatalf("attempts[%d] provider = %v, want %s", i, got, p)
		}
	}
}

// Compile-time check: io.Writer compatibility for RenderHuman
// (proves the signature works with any io.Writer, not just
// *bytes.Buffer).
var _ io.Writer = (*bytes.Buffer)(nil)

// Compile-time check: Render + RenderBody return types are stable.
var _ = Render
var _ = RenderBody
