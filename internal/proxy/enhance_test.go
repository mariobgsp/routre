package proxy

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
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

// cfgOne builds a single-provider config JSON with the given kind and cache
// fragment, pointing at the given mock URL.
func cfgOne(m *mock.Server, kind, envName, cacheJSON string) string {
	return `{"listen":"127.0.0.1:0","tiers":[{"name":"t","providers":[{"name":"p","kind":"` + kind + `","base_url":"` + m.URL() + `/v1","api_key_env":"` + envName + `","models":["m"]}]}],"rtk":{"enabled":false},"cache":{` + cacheJSON + `}}`
}

// TestPromptCacheInjectionAnthropic: with cache.prompt_cache on and an
// anthropic outbound target, the gateway must inject cache_control
// breakpoints (system + last message) into the upstream body, and pass an
// already-present breakpoint through untouched.
func TestPromptCacheInjectionAnthropic(t *testing.T) {
	a, _ := mock.New("a")
	defer a.Close()
	t.Setenv("PC_KEY", "k")
	base, _ := testEnv(t, cfgOne(a, "anthropic", "PC_KEY", `"enabled":true,"prefix_order":false,"prompt_cache":true`))

	resp, _ := post(t, base, "/v1/messages", anthropicBody())
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var upstream map[string]any
	if err := json.Unmarshal(a.Body(), &upstream); err != nil {
		t.Fatalf("upstream body not JSON: %v", err)
	}
	sys, _ := upstream["system"].([]any)
	if len(sys) == 0 {
		t.Fatal("system should be an array after injection")
	}
	first := sys[0].(map[string]any)
	if cc, ok := first["cache_control"]; !ok || cc.(map[string]any)["type"] != "ephemeral" {
		t.Fatalf("system block missing cache_control: %+v", first)
	}
	msgs := upstream["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)
	content := last["content"].([]any)
	lastBlk := content[len(content)-1].(map[string]any)
	if cc, ok := lastBlk["cache_control"]; !ok || cc.(map[string]any)["type"] != "ephemeral" {
		t.Fatalf("last message block missing cache_control: %+v", lastBlk)
	}
}

// TestPromptCacheInjectionDisabled: with prompt_cache off, the upstream
// body must pass through without any cache_control injected.
func TestPromptCacheInjectionDisabled(t *testing.T) {
	a, _ := mock.New("a")
	defer a.Close()
	t.Setenv("PC_KEY2", "k")
	base, _ := testEnv(t, cfgOne(a, "anthropic", "PC_KEY2", `"enabled":true,"prefix_order":false,"prompt_cache":false`))

	resp, _ := post(t, base, "/v1/messages", anthropicBody())
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if strings.Contains(string(a.Body()), "cache_control") {
		t.Fatalf("prompt_cache off must not inject cache_control: %s", a.Body())
	}
}

// TestAuthRefreshRetry: provider returns 401; the gateway re-reads the env
// key file, finds a rotated key, and retries the SAME provider successfully
// — no failover needed.
func TestAuthRefreshRetry(t *testing.T) {
	a, _ := mock.New("a")
	defer a.Close()
	// Initial (stale) key in the process env.
	t.Setenv("ROT_KEY", "old-secret")
	// Rotated key file next to the config.
	cfgPath := writeConfigFile(t, cfgOne(a, "openai", "ROT_KEY", `"enabled":false`))
	envPath := filepath.Join(filepath.Dir(cfgPath), "routre.env")
	if err := os.WriteFile(envPath, []byte("ROT_KEY=new-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	st := config.NewStore(cfgPath)
	if err := st.Load(); err != nil {
		t.Fatalf("config load: %v", err)
	}
	cfg := st.Get()
	rtr := router.New(tiersFromConfig(cfg), router.DefaultCooldownPolicy())
	cch := cache.New(cache.Config{Enabled: cfg.Cache.Enabled, MaxEntries: cfg.Cache.MaxEntries, TTLSeconds: cfg.Cache.TTLSeconds, PrefixOrder: cfg.Cache.PrefixOrder})
	tk := rtk.New(rtk.Config{Enabled: cfg.RTK.Enabled, MinBytes: cfg.RTK.MinBytes, MaxBytes: cfg.RTK.MaxBytes})
	logger := log.New(io.Discard, "", 0)
	h := NewHandlers(st, rtr, cch, tk, logger, usage.New(""))
	srv := New(h, logger)
	ln, err := srv.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Shutdown(2 * time.Second) })
	base := "http://" + ln.Addr().String()

	a.SetFailOnce(401)
	resp, data := post(t, base, "/v1/chat/completions", chatBody(false, ""))
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200 after refresh-retry, got %d: %s", resp.StatusCode, data)
	}
	if a.Requests() != 2 {
		t.Fatalf("expected one 401 + one refresh retry on the same provider, got %d requests", a.Requests())
	}
}

// TestRetryAfterHonoredOnFailover: a failing provider returning Retry-After
// on its 429 is put into cooldown; the request fails over to b.
func TestRetryAfterHonoredOnFailover(t *testing.T) {
	a, _ := mock.New("a")
	defer a.Close()
	b, _ := mock.New("b")
	defer b.Close()
	t.Setenv("RA_KEY_A", "ka")
	t.Setenv("RA_KEY_B", "kb")
	base, _ := testEnv(t, buildConfigWithMocks(t, map[string]*mock.Server{"a": a, "b": b}))

	a.SetFail(429)
	a.FailRetryAfter = "30"

	resp, data := post(t, base, "/v1/chat/completions", chatBody(false, ""))
	if resp.StatusCode != 200 {
		t.Fatalf("expected failover to b, got %d: %s", resp.StatusCode, data)
	}
	if got := resp.Header.Get("X-Llrouter-Provider"); got != "b" {
		t.Fatalf("expected b to serve, got %q", got)
	}
	for _, s := range routerStatus(t, base) {
		if s.Provider == "a" && s.CooldownRemaining <= 0 {
			t.Fatalf("provider a should be cooling down after Retry-After 429: %+v", s)
		}
	}
}

// anthropicBody builds a minimal /v1/messages request with a string system
// and a block-array user message so the injection has both targets.
func anthropicBody() []byte {
	return []byte(`{"model":"m","system":"You are a helpful assistant.","messages":[{"role":"user","content":[{"type":"text","text":"what is the weather?"}]}]}`)
}
