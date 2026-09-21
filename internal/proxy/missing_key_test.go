package proxy

import (
	"net/http"
	"strings"
	"testing"

	"github.com/mariobgsp/routre/internal/mock"
)

// missingKeyConfig points p0 at an API key env var that is never set and p1 at
// a healthy key. testEnv seeds TEST_KEY_A/B/C, so TEST_KEY_MISSING stays unset.
func missingKeyConfig(p0URL, p1URL string) string {
	return `{"listen":"127.0.0.1:0","tiers":[{"name":"t","providers":[` +
		`{"name":"p0","kind":"openai","base_url":"` + p0URL + `/v1","api_key_env":"TEST_KEY_MISSING","models":["m"]},` +
		`{"name":"p1","kind":"openai","base_url":"` + p1URL + `/v1","api_key_env":"TEST_KEY_B","models":["m"]}` +
		`]}],"rtk":{"enabled":false},"cache":{"enabled":false}}`
}

// TestMissingProviderKeyFailsOverWithoutCooldown: a provider whose key env var
// is unset is a config error, not an upstream failure. It must fail over to a
// provider that has its key, be never contacted, and never be cooldowned (the
// old behaviour swept every provider and put each in a 2s..5min cooldown).
func TestMissingProviderKeyFailsOverWithoutCooldown(t *testing.T) {
	missing, err := mock.New("p0")
	if err != nil {
		t.Fatal(err)
	}
	defer missing.Close()
	good, err := mock.New("p1")
	if err != nil {
		t.Fatal(err)
	}
	defer good.Close()
	base, _ := testEnv(t, missingKeyConfig(missing.URL(), good.URL()))

	resp, data := post(t, base, "/v1/chat/completions", chatBody(false, ""))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200 from the provider that has its key, got %d: %s", resp.StatusCode, data)
	}
	if got := resp.Header.Get("X-Llrouter-Provider"); got != "p1" {
		t.Fatalf("want p1 to serve, got %q", got)
	}
	if n := missing.Requests(); n != 0 {
		t.Errorf("a provider with no key was contacted %d times", n)
	}
	for _, s := range routerStatus(t, base) {
		if s.Provider != "p0" {
			continue
		}
		if s.Failures != 0 {
			t.Errorf("p0 failures = %d, want 0 (a config typo must not escalate)", s.Failures)
		}
		if s.CooldownRemaining != 0 {
			t.Errorf("p0 cooldown = %v, want 0", s.CooldownRemaining)
		}
	}
}

// TestMissingProviderKeyAllMissingIsHonest503: every provider misconfigured.
// The client gets an actionable 503 naming the env var, never a
// model_not_found 404.
func TestMissingProviderKeyAllMissingIsHonest503(t *testing.T) {
	a, err := mock.New("p0")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := mock.New("p1")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	cfg := `{"listen":"127.0.0.1:0","tiers":[{"name":"t","providers":[` +
		`{"name":"p0","kind":"openai","base_url":"` + a.URL() + `/v1","api_key_env":"TEST_KEY_MISSING","models":["m"]},` +
		`{"name":"p1","kind":"openai","base_url":"` + b.URL() + `/v1","api_key_env":"TEST_KEY_MISSING","models":["m"]}` +
		`]}],"rtk":{"enabled":false},"cache":{"enabled":false}}`
	base, _ := testEnv(t, cfg)

	resp, data := post(t, base, "/v1/chat/completions", chatBody(false, ""))
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d: %s", resp.StatusCode, data)
	}
	if !strings.Contains(string(data), `"class":"config"`) {
		t.Errorf("expected a class=config attempt entry, got: %s", data)
	}
	if !strings.Contains(string(data), "TEST_KEY_MISSING") {
		t.Errorf("the body must name the missing env var, got: %s", data)
	}
	if strings.Contains(string(data), "model_not_found") {
		t.Errorf("a key typo must not render as model_not_found: %s", data)
	}
	if a.Requests() != 0 || b.Requests() != 0 {
		t.Errorf("no provider should be contacted without a key (a=%d b=%d)", a.Requests(), b.Requests())
	}
}

// TestMissingProviderKeyStreamFailover: the streaming path must fail over the
// same way, without contacting the keyless provider.
func TestMissingProviderKeyStreamFailover(t *testing.T) {
	missing, err := mock.New("p0")
	if err != nil {
		t.Fatal(err)
	}
	defer missing.Close()
	good, err := mock.New("p1")
	if err != nil {
		t.Fatal(err)
	}
	defer good.Close()
	base, _ := testEnv(t, missingKeyConfig(missing.URL(), good.URL()))

	resp, data := post(t, base, "/v1/chat/completions", chatBody(true, ""))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200 from the streaming provider that has its key, got %d: %s", resp.StatusCode, data)
	}
	if n := missing.Requests(); n != 0 {
		t.Errorf("a provider with no key was contacted %d times", n)
	}
	if n := good.Requests(); n != 1 {
		t.Errorf("the healthy streaming provider was contacted %d times, want 1", n)
	}
}
