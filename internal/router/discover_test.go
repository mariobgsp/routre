package router

import (
	"testing"

	"github.com/mariobgsp/routre/internal/mock"
)

// TestDiscoverPopulatesEmptyProvider: discovery fills a provider's candidate
// set when config models are empty.
func TestDiscoverPopulatesEmptyProvider(t *testing.T) {
	srv, err := mock.New("disc")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	srv.SetModels([]string{"model-a", "model-b"})

	r := New([]TierInput{{Name: "t", Providers: []ProviderInput{
		{Name: "disc", Kind: "openai", BaseURL: srv.URL() + "/v1", APIKeyEnv: "K", Models: nil},
	}}}, DefaultCooldownPolicy())

	if n := r.DiscoverModels(nil, nil); n != 1 {
		t.Fatalf("expected 1 provider refreshed, got %d", n)
	}
	if len(r.Candidates("model-a")) == 0 {
		t.Fatal("model-a must be discoverable after discovery")
	}
	if len(r.Candidates("model-b")) == 0 {
		t.Fatal("model-b must be discoverable after discovery")
	}
	if len(r.Candidates("no-such")) != 0 {
		t.Fatal("unknown model must not become a candidate")
	}
}

// TestDiscoverKeepsExplicitModels: a provider with explicit models keeps them
// AND gains the discovered ones.
func TestDiscoverKeepsExplicitModels(t *testing.T) {
	srv, _ := mock.New("disc")
	defer srv.Close()
	srv.SetModels([]string{"discovered-1"})

	r := New([]TierInput{{Name: "t", Providers: []ProviderInput{
		{Name: "disc", Kind: "openai", BaseURL: srv.URL() + "/v1", APIKeyEnv: "K", Models: []string{"explicit-1"}},
	}}}, DefaultCooldownPolicy())

	r.DiscoverModels(nil, nil)
	if len(r.Candidates("explicit-1")) == 0 {
		t.Fatal("explicit model must be preserved")
	}
	if len(r.Candidates("discovered-1")) == 0 {
		t.Fatal("discovered model must be added alongside explicit ones")
	}
}

// TestDiscoverUnreachableFallsBack: an unreachable/refusing provider falls
// back to its explicit config (possibly empty) without erroring.
func TestDiscoverUnreachableFallsBack(t *testing.T) {
	srv, _ := mock.New("disc")
	badURL := srv.URL() + "/v1"
	srv.Close() // refuse connections

	warned := false
	r := New([]TierInput{{Name: "t", Providers: []ProviderInput{
		{Name: "disc", Kind: "openai", BaseURL: badURL, APIKeyEnv: "K", Models: []string{"explicit-1"}},
	}}}, DefaultCooldownPolicy())

	if n := r.DiscoverModels(nil, func(name string, _ error) { warned = true }); n != 0 {
		t.Fatalf("expected 0 providers refreshed, got %d", n)
	}
	if !warned {
		t.Fatal("expected a warning callback on discovery failure")
	}
	if len(r.Candidates("explicit-1")) == 0 {
		t.Fatal("explicit config must survive a discovery failure")
	}
	// And a discovery 404 (served but unusable) must also fall back cleanly.
	srv2, _ := mock.New("disc404")
	defer srv2.Close()
	srv2.SetModels(nil) // empty list -> empty discovery, no error
	r2 := New([]TierInput{{Name: "t", Providers: []ProviderInput{
		{Name: "disc404", Kind: "openai", BaseURL: srv2.URL() + "/v1", APIKeyEnv: "K", Models: nil},
	}}}, DefaultCooldownPolicy())
	if n := r2.DiscoverModels(nil, nil); n != 1 {
		t.Fatalf("empty model list is a success (1 refresh), got %d", n)
	}
}

// TestDiscoverSeesCandidates: discovered models appear via router candidates
// (the path providerServes/Candidates consume).
func TestDiscoverSeesCandidates(t *testing.T) {
	srv, _ := mock.New("disc")
	defer srv.Close()
	srv.SetModels([]string{"shared"})

	r := New([]TierInput{{Name: "t", Providers: []ProviderInput{
		{Name: "disc", Kind: "openai", BaseURL: srv.URL() + "/v1", APIKeyEnv: "K", Models: nil},
	}}}, DefaultCooldownPolicy())
	r.DiscoverModels(nil, nil)

	if !r.ServesModel("shared") {
		t.Fatal("ServesModel must see discovered models")
	}
	cands := r.Candidates("shared")
	if len(cands) == 0 || cands[0].Provider.Provider.Name != "disc" {
		t.Fatalf("discovered model must be a real candidate: %+v", cands)
	}
}
