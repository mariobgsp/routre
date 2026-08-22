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
// serving the model, and reports whether any provider serves it. With
// forwardUnknown=true every provider counts as a potential server of an
// unlisted model.
func TestMinCooldownForModel(t *testing.T) {
	r := New(mkTiers(), DefaultCooldownPolicy())

	// No provider serves the model.
	if _, found := r.MinCooldownForModel("m-nonexistent", false); found {
		t.Fatal("model configured nowhere must not be found")
	}

	// Only provider a serves m1; cooling it down yields its remaining
	// cooldown and found=true.
	a := r.Next(0)
	r.ReportFailure(a, ErrServer)
	rem, found := r.MinCooldownForModel("m1", false)
	if !found {
		t.Fatal("m1 is served by provider a even while cooling down")
	}
	if rem <= 0 {
		t.Fatalf("expected a positive cooldown for m1, got %v", rem)
	}

	// forwardUnknown=true: an unlisted model counts every provider as a
	// potential server, so a cooling-down fleet reports Retry-After instead
	// of model_not_found identity.
	_, found = r.MinCooldownForModel("m-nonexistent", true)
	if !found {
		t.Fatal("forwardUnknown must treat any provider as a potential server")
	}
}

func TestCandidatesQualifiedModel(t *testing.T) {
	r := New(mkModelTiers(), DefaultCooldownPolicy())
	// Client may send "opencode-go/deepseek-v4-flash"; the tail must match.
	cands := r.Candidates("opencode-go/deepseek-v4-flash")
	if len(cands) == 0 || cands[0].Provider.Provider.Name != "opencode-go" {
		t.Fatalf("qualified model must match opencode-go: %+v", cands)
	}
	// The upstream must receive the bare listed name, not the prefixed
	// client string (opencode.ai rejects "opencode-go/gpt-5.6-luna").
	if got := cands[0].Upstream; got != "deepseek-v4-flash" {
		t.Fatalf("qualified model must be unwrapped upstream, got %q", got)
	}
	// OpenRouter-style lists carry the org prefix as the actual listed
	// name; a prefixed request must resolve to the listed entry verbatim,
	// and multi-slash IDs must never be tail-split.
	or := New([]TierInput{{Name: "t", Providers: []ProviderInput{
		{Name: "openrouter", Kind: "openai", BaseURL: "https://or", APIKeyEnv: "K",
			Models: []string{"openai/gpt-5.6-luna", "openai/gpt-oss-20b:free", "deepseek/deepseek-chat"}},
	}}}, DefaultCooldownPolicy())
	cands = or.Candidates("openai/gpt-5.6-luna")
	if len(cands) == 0 || cands[0].Upstream != "openai/gpt-5.6-luna" {
		t.Fatalf("OpenRouter listed name must pass through: %+v", cands)
	}
	// openrouter/openai/gpt-5.6-luna: strip only the routing label, keep
	// the rest of the ID intact.
	cands = or.Candidates("openrouter/openai/gpt-5.6-luna")
	if len(cands) == 0 || cands[0].Upstream != "openai/gpt-5.6-luna" {
		t.Fatalf("multi-slash ID must strip only first segment: %+v", cands)
	}
	// Multi-slash with free suffix must stay intact after the label; when
	// the client already asked for the free variant, that name IS upstream.
	cands = or.Candidates("openrouter/openai/gpt-oss-20b:free")
	if len(cands) == 0 || cands[0].Upstream != "openai/gpt-oss-20b:free" {
		t.Fatalf("free variant must be preferred and intact: %+v", cands)
	}
	// Unknown first segment that no provider lists: no candidate (upstream
	// is authoritative only when the full string is itself a listed name).
	if cands := or.Candidates("someother/openai/gpt-5.6-luna"); len(cands) != 0 {
		t.Fatalf("untracked prefix must produce no candidates: %+v", cands)
	}
	cands = or.Candidates("gpt-oss-20b")
	if len(cands) == 0 || cands[0].Upstream != "openai/gpt-oss-20b:free" {
		t.Fatalf("free variant must be preferred: %+v", cands)
	}
}

func TestCandidatesForwardsUnknownModel(t *testing.T) {
	r := New(mkModelTiers(), DefaultCooldownPolicy())
	r.SetForwardUnknown(true)
	// A model configured nowhere must still be forwarded verbatim to every
	// available provider (tier order) when forward_unknown is on.
	cands := r.Candidates("future-model-x")
	if len(cands) != 3 {
		t.Fatalf("forward_unknown must forward to all 3 providers, got %d: %+v", len(cands), cands)
	}
	for _, c := range cands {
		if c.Upstream != "future-model-x" {
			t.Fatalf("unknown model must be forwarded verbatim, got %q", c.Upstream)
		}
		if !c.IsWildcard {
			t.Fatalf("forwarded candidate must be flagged IsWildcard: %+v", c)
		}
	}
}

func TestCandidatesUnknownModelStrict(t *testing.T) {
	r := New(mkModelTiers(), DefaultCooldownPolicy())
	r.SetForwardUnknown(false)
	// Strict mode preserves the original 402-cascade guard.
	if cands := r.Candidates("future-model-x"); len(cands) != 0 {
		t.Fatalf("strict mode must return no candidates for unknown model: %+v", cands)
	}
}

func TestCandidatesUnknownModelSkipsCooldown(t *testing.T) {
	r := New(mkModelTiers(), DefaultCooldownPolicy())
	r.SetForwardUnknown(true)
	// A provider in cooldown must not be a wildcard candidate.
	a := r.Next(0) // opencode-go
	r.ReportFailure(a, ErrServer)
	cands := r.Candidates("future-model-x")
	if len(cands) != 2 {
		t.Fatalf("cooled-down provider must be skipped, got %d: %+v", len(cands), cands)
	}
	for _, c := range cands {
		if c.Provider.Provider.Name == "opencode-go" {
			t.Fatal("opencode-go is in cooldown and must be skipped")
		}
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

// TestReportFailureWithBackoffHonorsRetryAfter: an upstream Retry-After
// acts as a FLOOR on the cooldown, never a ceiling — a long RA dominates
// the default backoff, a short RA never shortens it. Uses the injected
// clock so no real sleeps are needed.
func TestReportFailureWithBackoffHonorsRetryAfter(t *testing.T) {
	cur := time.Unix(1700000000, 0)
	r := New(mkModelTiers(), DefaultCooldownPolicy())
	r.now = func() time.Time { return cur }
	target := r.provs[0]

	// Fresh provider: default single-failure backoff is the 2s base. A 30s
	// Retry-After must extend it to ~30s.
	r.ReportFailureWithBackoff(target, ErrRateLimit, 30*time.Second)
	if rem := r.CooldownRemaining(target); rem <= 0 || rem > 30*time.Second {
		t.Fatalf("expected cooldown ~30s (RA floor), got %v", rem)
	}

	// Fresh provider again: a 1s Retry-After must NOT shorten the 2s base
	// backoff — the floor rule holds for short RAs too.
	cur = cur.Add(31 * time.Second)
	r.ReportSuccess(target) // reset so the next failure is a single (2s base)
	if rem := r.CooldownRemaining(target); rem != 0 {
		t.Fatalf("expected no cooldown after ReportSuccess, got %v", rem)
	}
	r.ReportFailureWithBackoff(target, ErrRateLimit, 1*time.Second)
	if rem := r.CooldownRemaining(target); rem <= 0 || rem > 2*time.Second+time.Second {
		t.Fatalf("expected cooldown ~2s (2s base > 1s RA), got %v", rem)
	}
}
