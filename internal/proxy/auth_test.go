package proxy

import (
	"io"
	"log"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mariobgsp/routre/internal/cache"
	"github.com/mariobgsp/routre/internal/config"
	"github.com/mariobgsp/routre/internal/mock"
	"github.com/mariobgsp/routre/internal/router"
	"github.com/mariobgsp/routre/internal/rtk"
	"github.com/mariobgsp/routre/internal/usage"
)

// authEnv wires a gateway with auth enabled (secret env AUTH_KEY, value
// "super-secret") backed by an openai mock. processToken, if non-empty, is
// the per-process CLI token. Returns the base URL.
func authEnv(t *testing.T, processToken string) (base string, m *mock.Server) {
	t.Helper()
	t.Setenv("AUTH_KEY", "super-secret")
	t.Setenv("TEST_KEY", "k")
	m, err := mock.New("a")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	cfgJSON := `{"listen":"127.0.0.1:0","rtk":{"enabled":false},"cache":{"enabled":false},"auth":{"secret_env":"AUTH_KEY","header":"X-Routre-Key"},"tiers":[{"name":"t","providers":[{"name":"a","kind":"openai","base_url":"` + m.URL() + `/v1","api_key_env":"TEST_KEY","models":["m"]}]}]}`
	cfgPath := writeConfigFile(t, cfgJSON)
	st := config.NewStore(cfgPath)
	if err := st.Load(); err != nil {
		t.Fatalf("config load: %v", err)
	}
	cfg := st.Get()
	rtr := router.New(tiersFromConfig(cfg), router.DefaultCooldownPolicy())
	cch := cache.New(cache.Config{Enabled: false})
	tk := rtk.New(rtk.Config{Enabled: false})
	logger := log.New(io.Discard, "", 0)
	h := NewHandlers(st, rtr, cch, tk, logger, usage.New(""))
	// Seed the auth secret into the keystore (as serve does).
	h.Keys.Set("AUTH_KEY", "super-secret")
	srv := New(h, logger)
	if processToken != "" {
		srv.SetProcessToken(processToken)
	}
	ln, err := srv.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Shutdown(2 * time.Second) })
	return "http://" + ln.Addr().String(), m
}

func TestAuthRejectsMissing(t *testing.T) {
	base, m := authEnv(t, "")
	resp, data := post(t, base, "/v1/chat/completions", []byte(`{"model":"m","messages":[]}`))
	if resp.StatusCode != 401 {
		t.Fatalf("status = %d, want 401: %s", resp.StatusCode, data)
	}
	if m.Requests() != 0 {
		t.Fatalf("upstream must not be hit on reject, got %d", m.Requests())
	}
}

func TestAuthAcceptsHeader(t *testing.T) {
	base, _ := authEnv(t, "")
	resp, _ := postAuth(t, base, "X-Routre-Key", "super-secret")
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestAuthAcceptsBearerAlias(t *testing.T) {
	base, _ := authEnv(t, "")
	resp, _ := postAuth(t, base, "Authorization", "Bearer super-secret")
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestAuthWrongSecret(t *testing.T) {
	base, _ := authEnv(t, "")
	resp, _ := postAuth(t, base, "X-Routre-Key", "wrong")
	if resp.StatusCode != 401 {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestAuthProcessToken(t *testing.T) {
	base, _ := authEnv(t, "cli-token-123")
	resp, _ := postAuth(t, base, "X-Routre-Key", "cli-token-123")
	if resp.StatusCode != 200 {
		t.Fatalf("process token status = %d, want 200", resp.StatusCode)
	}
	// The shared secret still works too.
	resp2, _ := postAuth(t, base, "X-Routre-Key", "super-secret")
	if resp2.StatusCode != 200 {
		t.Fatalf("shared secret status = %d, want 200", resp2.StatusCode)
	}
}

func TestAuthHealthzExempt(t *testing.T) {
	base, _ := authEnv(t, "")
	resp, _ := http.Get(base + "/healthz")
	if resp.StatusCode != 200 {
		t.Fatalf("healthz status = %d, want 200 (exempt)", resp.StatusCode)
	}
}

func TestAuthDisabledPassthrough(t *testing.T) {
	// No auth block: requests pass through byte-identically (401-free).
	m, err := mock.New("a")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	t.Setenv("TEST_KEY", "k")
	cfgJSON := `{"listen":"127.0.0.1:0","rtk":{"enabled":false},"cache":{"enabled":false},"tiers":[{"name":"t","providers":[{"name":"a","kind":"openai","base_url":"` + m.URL() + `/v1","api_key_env":"TEST_KEY","models":["m"]}]}]}`
	cfgPath := writeConfigFile(t, cfgJSON)
	st := config.NewStore(cfgPath)
	if err := st.Load(); err != nil {
		t.Fatal(err)
	}
	cfg := st.Get()
	h := NewHandlers(st, router.New(tiersFromConfig(cfg), router.DefaultCooldownPolicy()), cache.New(cache.Config{Enabled: false}), rtk.New(rtk.Config{Enabled: false}), log.New(io.Discard, "", 0), usage.New(""))
	srv := New(h, log.New(io.Discard, "", 0))
	ln, _ := srv.Listen("127.0.0.1:0")
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Shutdown(2 * time.Second) })
	base := "http://" + ln.Addr().String()
	resp, _ := post(t, base, "/v1/chat/completions", []byte(`{"model":"m","messages":[]}`))
	if resp.StatusCode != 200 {
		t.Fatalf("auth-disabled status = %d, want 200", resp.StatusCode)
	}
}

func TestSecureEqual(t *testing.T) {
	if !secureEqual("abc", "abc") {
		t.Fatal("equal strings should match")
	}
	if secureEqual("abc", "abd") {
		t.Fatal("differing strings should not match")
	}
	if secureEqual("abc", "abcd") {
		t.Fatal("differing lengths should not match")
	}
}

// postAuth posts a chat completion with a specific header set.
func postAuth(t *testing.T, base, header, value string) (*http.Response, []byte) {
	t.Helper()
	body := strings.NewReader(`{"model":"m","messages":[]}`)
	req, err := http.NewRequest(http.MethodPost, base+"/v1/chat/completions", body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(header, value)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp, data
}
