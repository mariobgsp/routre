package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/mariobgsp/routre/internal/mock"
)

// geminiEnv wires a gateway with a single gemini-kind provider backed by a
// Gemini-mode mock upstream and returns its base URL.
func geminiEnv(t *testing.T) (base string, m *mock.Server) {
	t.Helper()
	t.Setenv("GEM_KEY", "gk")
	m, err := mock.New("g")
	if err != nil {
		t.Fatal(err)
	}
	m.SetGemini(true)
	t.Cleanup(m.Close)
	cfgJSON := `{"listen":"127.0.0.1:0","rtk":{"enabled":false},"cache":{"enabled":false},"tiers":[{"name":"t","providers":[{"name":"gem","kind":"gemini","base_url":"` + m.URL() + `","api_key_env":"GEM_KEY","models":["gemini-pro"]}]}]}`
	base, _, _ = serveGateway(t, loadTestStore(t, cfgJSON))
	return base, m
}

func TestGeminiNonStreamingRelay(t *testing.T) {
	base, _ := geminiEnv(t)
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
	base, _ := geminiEnv(t)
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

func TestGeminiViaAnthropicClientNonStreaming(t *testing.T) {
	base, _ := geminiEnv(t)
	body := `{"model":"gemini-pro","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`
	resp, data := post(t, base, "/v1/messages", []byte(body))
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, data)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["type"] != "message" || doc["role"] != "assistant" {
		t.Fatalf("envelope = %v/%v", doc["type"], doc["role"])
	}
	content := doc["content"].([]any)
	blk := content[0].(map[string]any)
	if blk["type"] != "text" || !strings.Contains(blk["text"].(string), "gemini response") {
		t.Fatalf("content block = %v", blk)
	}
	if doc["stop_reason"] != "end_turn" {
		t.Fatalf("stop_reason = %v", doc["stop_reason"])
	}
	if usage, ok := doc["usage"].(map[string]any); !ok || usage["input_tokens"] != float64(10) || usage["output_tokens"] != float64(5) {
		t.Fatalf("usage = %v", doc["usage"])
	}
}

func TestGeminiViaAnthropicClientStreaming(t *testing.T) {
	base, _ := geminiEnv(t)
	body := `{"model":"gemini-pro","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
	req, err := http.NewRequest("POST", base+"/v1/messages", bytes.NewBufferString(body))
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
	for _, want := range []string{
		`"type":"message_start"`,
		`"type":"content_block_start"`,
		`"type":"text_delta","text":"from-g"`,
		`"type":"content_block_stop"`,
		`"stop_reason":"end_turn"`,
		`"type":"message_stop"`,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %s in:\n%s", want, s)
		}
	}
}
