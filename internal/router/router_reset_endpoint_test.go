package router

import "testing"

// TestRouterResetClearsCooldownWhenEndpointChanges: a reload that points the
// same provider NAME at a different kind/base_url is a different upstream, so
// it must start fresh instead of inheriting the old endpoint's cooldown (an
// operator who fixes a broken endpoint must not see it stay benched).
func TestRouterResetClearsCooldownWhenEndpointChanges(t *testing.T) {
	r := New(mkModelTiers(), DefaultCooldownPolicy())
	cands := r.Candidates("hy3")
	if len(cands) == 0 {
		t.Fatal("setup: no candidates")
	}
	p := cands[0].Provider
	name := p.Provider.Name
	r.ReportFailure(p, ErrServer)
	if r.CooldownRemaining(p) <= 0 {
		t.Fatal("setup: expected a cooldown")
	}

	// Same name, repaired endpoint.
	changed := []TierInput{{Name: "subscription", Providers: []ProviderInput{
		{Name: name, Kind: "openai", BaseURL: "https://elsewhere", APIKeyEnv: "GO", Models: []string{"hy3"}},
	}}}
	r.Reset(changed, DefaultCooldownPolicy())

	found := false
	for _, s := range r.Status() {
		if s.Provider != name {
			continue
		}
		found = true
		if s.CooldownRemaining != 0 || s.Failures != 0 {
			t.Fatalf("repaired endpoint kept the old state: cooldown=%v failures=%d", s.CooldownRemaining, s.Failures)
		}
	}
	if !found {
		t.Fatalf("%s missing from Status after Reset", name)
	}
}

// TestRouterResetKeepsStateForUnchangedEndpoint is the counterpart: an
// identical (name, kind, base_url) must keep its cooldown across a reload.
func TestRouterResetKeepsStateForUnchangedEndpoint(t *testing.T) {
	tiers := mkModelTiers()
	r := New(tiers, DefaultCooldownPolicy())
	cands := r.Candidates("hy3")
	if len(cands) == 0 {
		t.Fatal("setup: no candidates")
	}
	p := cands[0].Provider
	r.ReportFailure(p, ErrServer)

	r.Reset(tiers, DefaultCooldownPolicy())
	if cd := r.CooldownRemaining(p); cd <= 0 {
		t.Fatal("an unchanged endpoint lost its cooldown across Reset")
	}
}
