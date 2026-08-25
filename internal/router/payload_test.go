package router

import (
	"encoding/json"
	"testing"
)

func TestCandidatePayloadClamps(t *testing.T) {
	// Setup router with a provider that has MaxTokens 131072
	tiers := []TierInput{
		{Name: "t", Providers: []ProviderInput{
			{Name: "a", Kind: "openai", BaseURL: "http://a", APIKeyEnv: "A", Models: []string{"m"}, MaxTokens: 131072},
		}},
	}
	r := New(tiers, DefaultCooldownPolicy())
	body := []byte(`{"model":"m","max_tokens":384000,"messages":[{"role":"user","content":"hello there friend"}]}`)
	cands := r.Candidates("m")
	if len(cands) == 0 {
		t.Fatal("no candidates")
	}
	got := cands[0].Payload(body, "m")
	var gotDoc map[string]any
	_ = json.Unmarshal(got, &gotDoc)
	if gotDoc["model"] != "m" {
		t.Fatalf("model not preserved: %v", gotDoc["model"])
	}
	gv, _ := gotDoc["max_tokens"].(float64)
	if gv >= 131072 || gv < 1024 {
		t.Fatalf("expected clamp 1024..131072, got %v", gv)
	}
	// Malformed body must pass through unchanged (fail-open)
	bad := []byte(`{"model":`)
	if got := cands[0].Payload(bad, "m"); string(got) != string(bad) {
		t.Fatalf("malformed must pass through")
	}
}

func TestCandidateShouldFailoverOnClientError(t *testing.T) {
	tiers := []TierInput{
		{Name: "t", Providers: []ProviderInput{
			{Name: "a", Kind: "openai", BaseURL: "http://a", APIKeyEnv: "A", Models: []string{"m"}},
		}},
	}
	r := New(tiers, DefaultCooldownPolicy())
	r.SetForwardUnknown(true)
	// Unknown model m2 should be wildcard
	cands := r.Candidates("m2")
	if len(cands) == 0 || !cands[0].ShouldFailoverOnClientError() {
		t.Fatal("wildcard should failover on client error")
	}
	cands2 := r.Candidates("m")
	if len(cands2) == 0 || cands2[0].ShouldFailoverOnClientError() {
		t.Fatal("non-wildcard should not failover")
	}
}
