package router

import (
	"errors"
	"testing"
	"time"
)

func mkTiers() []TierInput {
	return []TierInput{
		{Name: "subscription", Providers: []ProviderInput{
			{Name: "a", Kind: "anthropic", BaseURL: "https://a", APIKeyEnv: "A", Models: []string{"m1"}},
		}},
		{Name: "cheap", Providers: []ProviderInput{
			{Name: "b", Kind: "openai", BaseURL: "https://b", APIKeyEnv: "B", Models: []string{"m2"}},
			{Name: "c", Kind: "openai", BaseURL: "https://c", APIKeyEnv: "C", Models: []string{"m3"}},
		}},
		{Name: "free", Providers: []ProviderInput{
			{Name: "d", Kind: "openai", BaseURL: "https://d", APIKeyEnv: "D", Models: []string{"m4"}},
		}},
	}
}

func TestTierOrder(t *testing.T) {
	r := New(mkTiers(), DefaultCooldownPolicy())
	if p := r.Next(0); p == nil || p.Provider.Name != "a" {
		t.Fatalf("first provider must be tier-1: %+v", p)
	}
	if p := r.Next(1); p == nil || p.Provider.Name != "b" {
		t.Fatalf("second provider must be tier-2 first: %+v", p)
	}
}

func TestCooldownSkipsProvider(t *testing.T) {
	r := New(mkTiers(), DefaultCooldownPolicy())
	a := r.Next(0)
	r.ReportFailure(a, ErrServer)
	if p := r.Next(0); p == nil || p.Provider.Name != "b" {
		t.Fatalf("provider in cooldown must be skipped: %+v", p)
	}
}

func TestCooldownExpiry(t *testing.T) {
	r := New(mkTiers(), CooldownPolicy{Base: time.Millisecond, Max: time.Minute, MaxHits: 10})
	a := r.Next(0)
	r.ReportFailure(a, ErrServer)
	time.Sleep(5 * time.Millisecond)
	if p := r.Next(0); p == nil || p.Provider.Name != "a" {
		t.Fatal("provider must recover after cooldown")
	}
}

func TestExponentialBackoff(t *testing.T) {
	r := New(mkTiers(), DefaultCooldownPolicy())
	a := r.Next(0)
	r.ReportFailure(a, ErrServer)
	first := r.CooldownRemaining(a)
	r.ReportFailure(a, ErrServer)
	second := r.CooldownRemaining(a)
	if second <= first {
		t.Fatalf("backoff must grow: %v then %v", first, second)
	}
}

func TestBackoffCapped(t *testing.T) {
	r := New(mkTiers(), CooldownPolicy{Base: time.Second, Max: time.Minute, MaxHits: 100})
	a := r.Next(0)
	for i := 0; i < 20; i++ {
		r.ReportFailure(a, ErrServer)
	}
	if got := r.CooldownRemaining(a); got > time.Minute {
		t.Fatalf("backoff must cap at Max: %v", got)
	}
}

func TestSuccessResets(t *testing.T) {
	r := New(mkTiers(), DefaultCooldownPolicy())
	a := r.Next(0)
	r.ReportFailure(a, ErrServer)
	r.ReportSuccess(a)
	if got := r.CooldownRemaining(a); got != 0 {
		t.Fatalf("success must clear cooldown: %v", got)
	}
}

func TestStreamAbortDoesNotEscalate(t *testing.T) {
	r := New(mkTiers(), DefaultCooldownPolicy())
	a := r.Next(0)
	r.ReportFailure(a, ErrStream)
	if got := r.CooldownRemaining(a); got != 0 {
		t.Fatal("stream abort must not escalate cooldown")
	}
}

func TestRetryableClasses(t *testing.T) {
	for _, c := range []ErrClass{ErrNetwork, ErrTimeout, ErrRateLimit, ErrAuth, ErrServer} {
		if !IsRetryableClass(c) {
			t.Fatalf("%v must be retryable", c)
		}
	}
	for _, c := range []ErrClass{ErrClient, ErrStream} {
		if IsRetryableClass(c) {
			t.Fatalf("%v must NOT be retryable", c)
		}
	}
}

func TestClassifyStatus(t *testing.T) {
	cases := map[int]ErrClass{401: ErrAuth, 403: ErrAuth, 429: ErrRateLimit, 500: ErrServer, 502: ErrServer, 400: ErrClient, 404: ErrClient, 200: ErrClient}
	for status, want := range cases {
		if got := ClassifyStatus(status); got != want {
			t.Errorf("ClassifyStatus(%d) = %v, want %v", status, got, want)
		}
	}
}

func TestClassifyError(t *testing.T) {
	if Classify(StreamAborted()) != ErrStream {
		t.Fatal("stream abort must classify as ErrStream")
	}
	if Classify(errors.New("dial tcp: connection refused")) != ErrNetwork {
		t.Fatal("network error must classify as ErrNetwork")
	}
	if Classify(contextDeadlineExceeded) != ErrTimeout {
		t.Fatal("deadline error must classify as ErrTimeout")
	}
}

func TestAllExhausted(t *testing.T) {
	r := New(mkTiers(), DefaultCooldownPolicy())
	a := r.Next(0)
	r.ReportFailure(a, ErrServer)
	b := r.Next(1)
	r.ReportFailure(b, ErrServer)
	c := r.Next(2)
	r.ReportFailure(c, ErrServer)
	d := r.Next(3)
	r.ReportFailure(d, ErrServer)
	if p := r.Next(0); p != nil {
		t.Fatalf("all providers in cooldown: Next must return nil, got %+v", p)
	}
}

func mkModelTiers() []TierInput {
	// opencode-go serves the paid model; opencode-zen serves paid + free
	// variant; openrouter only the free variant. Tier order = preference.
	return []TierInput{
		{Name: "subscription", Providers: []ProviderInput{
			{Name: "opencode-go", Kind: "openai", BaseURL: "https://go", APIKeyEnv: "GO", Models: []string{"deepseek-v4-flash", "hy3"}},
			{Name: "opencode-zen", Kind: "openai", BaseURL: "https://zen", APIKeyEnv: "ZEN", Models: []string{"deepseek-v4-flash", "deepseek-v4-flash-free", "hy3", "hy3-free"}},
		}},
		{Name: "openrouter", Providers: []ProviderInput{
			{Name: "openrouter", Kind: "openai", BaseURL: "https://or", APIKeyEnv: "OR", Models: []string{"deepseek/deepseek-v4-flash:free"}},
		}},
	}
}

func TestCandidatesPrefersPaidTierThenFreeVariant(t *testing.T) {
	r := New(mkModelTiers(), DefaultCooldownPolicy())
	cands := r.Candidates("deepseek-v4-flash")
	if len(cands) != 3 {
		t.Fatalf("want 3 candidates, got %d: %+v", len(cands), cands)
	}
	// Preference order: opencode-go (paid) first, then opencode-zen's free
	// variant, then openrouter's free variant. The client asked for the
	// paid model, so the user's default provider is tried first; free
	// variants are the 2nd/3rd fallback.
	if cands[0].Provider.Provider.Name != "opencode-go" || cands[0].IsFree || cands[0].Upstream != "deepseek-v4-flash" {
		t.Fatalf("candidate 0 must be paid opencode-go: %+v", cands[0])
	}
	if cands[1].Provider.Provider.Name != "opencode-zen" || !cands[1].IsFree || cands[1].Upstream != "deepseek-v4-flash-free" {
		t.Fatalf("candidate 1 must be free variant on opencode-zen: %+v", cands[1])
	}
	if cands[2].Provider.Provider.Name != "openrouter" || !cands[2].IsFree || cands[2].Upstream != "deepseek/deepseek-v4-flash:free" {
		t.Fatalf("candidate 2 must be free variant on openrouter: %+v", cands[2])
	}
}

func TestCandidatesSkipsProvidersWithoutModel(t *testing.T) {
	r := New(mkModelTiers(), DefaultCooldownPolicy())
	// "hy3" is served paid by go and zen; openrouter has no hy3 at all.
	cands := r.Candidates("hy3")
	if len(cands) != 2 {
		t.Fatalf("want 2 candidates (no openrouter), got %d", len(cands))
	}
	for _, c := range cands {
		if c.Provider.Provider.Name == "openrouter" {
			t.Fatal("openrouter must not be a candidate for hy3")
		}
	}
}

// TestServesModel: ServesModel ignores cooldowns and reports whether any
// provider lists the model or a free variant of it.
func TestServesModel(t *testing.T) {
	r := New(mkTiers(), DefaultCooldownPolicy())
	if !r.ServesModel("m1") {
		t.Fatal("m1 is configured on provider a")
	}
	if r.ServesModel("m-nonexistent") {
		t.Fatal("m-nonexistent is configured nowhere")
	}
	// Cooldown must not affect ServesModel.
	a := r.Next(0)
	r.ReportFailure(a, ErrServer)
	if !r.ServesModel("m1") {
		t.Fatal("ServesModel must ignore cooldown state")
	}
}

// TestMinCooldownForModel: returns the shortest cooldown among providers
// serving the model, and reports whether any provider serves it.
func TestMinCooldownForModel(t *testing.T) {
	r := New(mkTiers(), DefaultCooldownPolicy())

	// No provider serves the model.
	if _, found := r.MinCooldownForModel("m-nonexistent"); found {
		t.Fatal("model configured nowhere must not be found")
	}

	// Only provider a serves m1; cooling it down yields its remaining
	// cooldown and found=true.
	a := r.Next(0)
	r.ReportFailure(a, ErrServer)
	rem, found := r.MinCooldownForModel("m1")
	if !found {
		t.Fatal("m1 is served by provider a even while cooling down")
	}
	if rem <= 0 {
		t.Fatalf("expected a positive cooldown for m1, got %v", rem)
	}
}

func TestCandidatesQualifiedModel(t *testing.T) {
	r := New(mkModelTiers(), DefaultCooldownPolicy())
	// Client may send "opencode-go/deepseek-v4-flash"; the tail must match.
	cands := r.Candidates("opencode-go/deepseek-v4-flash")
	if len(cands) == 0 || cands[0].Provider.Provider.Name != "opencode-go" {
		t.Fatalf("qualified model must match opencode-go: %+v", cands)
	}
}

func TestCreditsFailureDoesNotCooldown(t *testing.T) {
	r := New(mkModelTiers(), DefaultCooldownPolicy())
	zen := r.Next(1)
	r.ReportFailure(zen, ErrCredits)
	if got := r.CooldownRemaining(zen); got != 0 {
		t.Fatalf("credits failure must not escalate cooldown, got %v", got)
	}
	// And the provider must still be a candidate (free variants may work).
	cands := r.Candidates("deepseek-v4-flash")
	found := false
	for _, c := range cands {
		if c.Provider.Provider.Name == "opencode-zen" {
			found = true
		}
	}
	if !found {
		t.Fatal("opencode-zen must remain a candidate after a credits failure")
	}
}

func TestClassifyStatusBodyCredits(t *testing.T) {
	if got := ClassifyStatusBody(401, []byte(`{"type":"error","error":{"type":"CreditsError","message":"Insufficient balance"}}`)); got != ErrCredits {
		t.Fatalf("401 with CreditsError body must classify as ErrCredits, got %v", got)
	}
	if got := ClassifyStatusBody(402, nil); got != ErrCredits {
		t.Fatalf("402 must classify as ErrCredits, got %v", got)
	}
	if got := ClassifyStatusBody(401, []byte(`{"error":"bad key"}`)); got != ErrAuth {
		t.Fatalf("plain 401 must stay ErrAuth, got %v", got)
	}
}
