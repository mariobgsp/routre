package router

import (
	"sync"
	"testing"
)

// TestRouterResetReusesProviderStatePointers: a config reload must not orphan
// the *ProviderState an in-flight request holds. After Reset the same pointer
// stays in the live list and its cooldown survives (the old implementation
// rebuilt the list from scratch, silently discarding every cooldown).
func TestRouterResetReusesProviderStatePointers(t *testing.T) {
	tiers := mkModelTiers()
	r := New(tiers, DefaultCooldownPolicy())
	cands := r.Candidates("hy3")
	if len(cands) == 0 {
		t.Fatal("setup: no candidates")
	}
	p := cands[0].Provider

	r.ReportFailure(p, ErrServer)
	if r.CooldownRemaining(p) <= 0 {
		t.Fatal("setup: expected a cooldown")
	}

	r.Reset(tiers, DefaultCooldownPolicy())

	if cd := r.CooldownRemaining(p); cd <= 0 {
		t.Fatal("Reset dropped the in-flight provider's cooldown (pointer orphaned)")
	}
	// The live list must hold that same object, not a fresh one.
	found := false
	for _, s := range r.Status() {
		if s.Provider == p.Provider.Name {
			found = true
			if s.CooldownRemaining <= 0 {
				t.Fatalf("live state for %s lost its cooldown after Reset", s.Provider)
			}
		}
	}
	if !found {
		t.Fatalf("%s missing from Status after Reset", p.Provider.Name)
	}
}

// TestRouterResetDropsRemovedProviders: providers removed from the config lose
// their state and the live list shrinks.
func TestRouterResetDropsRemovedProviders(t *testing.T) {
	r := New(mkModelTiers(), DefaultCooldownPolicy())
	cands := r.Candidates("hy3")
	if len(cands) == 0 {
		t.Fatal("setup: no candidates")
	}
	removedName := cands[0].Provider.Provider.Name
	r.ReportFailure(cands[0].Provider, ErrServer)

	r.Reset([]TierInput{{Name: "subscription", Providers: []ProviderInput{
		{Name: "opencode-zen", Kind: "openai", BaseURL: "https://zen", APIKeyEnv: "ZEN", Models: []string{"hy3"}},
	}}}, DefaultCooldownPolicy())

	if r.Len() != 1 {
		t.Fatalf("Len = %d, want 1", r.Len())
	}
	for _, s := range r.Status() {
		if s.Provider == removedName {
			t.Fatalf("removed provider %s still present after Reset", removedName)
		}
	}
}

// TestRouterResetConcurrentWithReports: Reset, ReportFailure/ReportSuccess and
// Status must be mutually safe (run under -race).
func TestRouterResetConcurrentWithReports(t *testing.T) {
	tiers := mkModelTiers()
	r := New(tiers, DefaultCooldownPolicy())
	cands := r.Candidates("hy3")
	if len(cands) == 0 {
		t.Fatal("setup: no candidates")
	}
	p := cands[0].Provider

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				r.ReportFailure(p, ErrServer)
				r.ReportSuccess(p)
				_ = r.CooldownRemaining(p)
				_ = r.Status()
			}
		}()
	}
	for i := 0; i < 10; i++ {
		r.Reset(tiers, DefaultCooldownPolicy())
	}
	close(stop)
	wg.Wait()
}
