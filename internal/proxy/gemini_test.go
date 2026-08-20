package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"testing"
	"time"

	"routre-cli/internal/cache"
	"routre-cli/internal/config"
	"routre-cli/internal/mock"
	"routre-cli/internal/router"
	"routre-cli/internal/rtk"
	"routre-cli/internal/usage"
)

// geminiEnv wires a gateway with a single gemini-kind provider backed by a
// Gemini-mode mock upstream and returns its base URL.
func geminiEnv(t *testing.T, streaming bool) (base string, m *mock.Server) {
	t.Helper()
	t.Setenv("GEM_KEY", "gk")
	m, err := mock.New("g")
	if err != nil {
		t.Fatal(err)
	}
	m.SetGemini(true)
	t.Cleanup(m.Close)
	cfgJSON := `{"listen":"127.0.0.1:0","rtk":{"enabled":false},"cache":{"enabled":false},"tiers":[{"name":"t","providers":[{"name":"gem","kind":"gemini","base_url":"` + m.URL() + `","api_key_env":"GEM_KEY","models":["gemini-pro"]}]}]}`
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
	srv := New(h, logger)
	ln, err := srv.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Shutdown(2 * time.Second) })
	return "http://" + ln.Addr().String(), m
}

func TestGeminiNonStreamingRelay(t *testing.T) {
	base, _ := geminiEnv(t, false)
	body := `{"model":"gemini-pro","messages":[{"role":"user","content":"hi"}]}`
	resp, data := post(t, base, "/v1/chat/completions", []byte(body))
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, data)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["object"] != "chat.completion" {
		t.Fatalf("object = %v", doc["object"])
	}
	choices := doc["choices"].([]any)
	ch := choices[0].(map[string]any)
	msg := ch["message"].(map[string]any)
	if !strings.Contains(msg["content"].(string), "gemini response") {
		t.Fatalf("content = %v", msg["content"])
	}
	if ch["finish_reason"] != "stop" {
		t.Fatalf("finish_reason = %v", ch["finish_reason"])
	}
	if usage, ok := doc["usage"].(map[string]any); ok && usage["prompt_tokens"] != float64(10) {
		t.Fatalf("usage = %v", usage)
	}
}

func TestGeminiStreamingRelay(t *testing.T) {
	base, _ := geminiEnv(t, true)
	body := `{"model":"gemini-pro","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	req, err := http.NewRequest("POST", base+"/v1/chat/completions", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	s := string(raw)
	if !strings.Contains(s, `"delta":{"content":"from-g"`) {
		t.Fatalf("missing gemini text delta:\n%s", s)
	}
	if !strings.Contains(s, `"finish_reason":"stop"`) {
		t.Fatalf("missing finish_reason:\n%s", s)
	}
	if !strings.Contains(s, "[DONE]") {
		t.Fatalf("missing [DONE]:\n%s", s)
	}
}
