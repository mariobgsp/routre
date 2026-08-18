package proxy

import (
	"encoding/json"
	"testing"
)

// TestBuildPayloadRewritesAndClamps: buildPayload must apply the model
// rewrite and the max_tokens clamp as field mutations on ONE decoded doc,
// then marshal once — the merged replacement for the old two-pass
// rewriteModel + clampMaxTokens byte-level re-encodes. We assert the
// observable result equals the sequential two-pass outcome.
func TestBuildPayloadRewritesAndClamps(t *testing.T) {
	body := []byte(`{"model":"m","max_tokens":384000,"messages":[{"role":"user","content":"hello there friend"}]}`)

	got := buildPayload(body, "m", "free-model", 131072)

	// Sequentially recompute the old behavior: rewrite model, then clamp.
	expected := map[string]any{}
	_ = json.Unmarshal(body, &expected)
	expected["model"] = "free-model"
	clampMaxTokens(expected, 131072)
	wantJSON, _ := json.Marshal(expected)

	gotDoc := map[string]any{}
	wantDoc := map[string]any{}
	_ = json.Unmarshal(got, &gotDoc)
	_ = json.Unmarshal(wantJSON, &wantDoc)

	if gotDoc["model"] != "free-model" {
		t.Fatalf("model not rewritten: %v", gotDoc["model"])
	}
	if gotN, _ := json.Marshal(gotDoc["max_tokens"]); string(gotN) != mustJSON(wantDoc["max_tokens"]) {
		t.Fatalf("max_tokens clamp diverged: got %v want %v", gotDoc["max_tokens"], wantDoc["max_tokens"])
	}
	gv, _ := gotDoc["max_tokens"].(float64)
	if gv >= 131072 || gv < 1024 {
		t.Fatalf("expected a meaningful prompt-aware clamp, got %v", gv)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// TestBuildPayloadFailsOpen: malformed bodies must pass through unchanged
// (same fail-open contract as the old byte-level helpers).
func TestBuildPayloadFailsOpen(t *testing.T) {
	bad := []byte(`{"model":`)
	if got := buildPayload(bad, "m", "m2", 131072); string(got) != string(bad) {
		t.Fatalf("malformed body must pass through unchanged, got %q", got)
	}
}
