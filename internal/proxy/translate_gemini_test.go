package proxy

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOpenAIToGemini(t *testing.T) {
	body := []byte(`{
		"model":"gemini-1.5-pro",
		"max_tokens":128,
		"temperature":0.5,
		"messages":[
			{"role":"system","content":"be brief"},
			{"role":"user","content":"hello"}
		],
		"tools":[{"type":"function","function":{"name":"bash","description":"run a shell","parameters":{"type":"object"}}}]
	}`)
	out, err := openAIToGemini(body)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["model"] != "gemini-1.5-pro" {
		t.Fatalf("model = %v", doc["model"])
	}
	si := doc["systemInstruction"].(map[string]any)
	if si["parts"] == nil {
		t.Fatal("missing systemInstruction.parts")
	}
	contents := doc["contents"].([]any)
	if len(contents) != 1 {
		t.Fatalf("contents len = %d", len(contents))
	}
	first := contents[0].(map[string]any)
	if first["role"] != "user" {
		t.Fatalf("role = %v", first["role"])
	}
	if doc["tools"] == nil {
		t.Fatal("missing tools")
	}
	gc := doc["generationConfig"].(map[string]any)
	if gc["maxOutputTokens"] != float64(128) || gc["temperature"] != 0.5 {
		t.Fatalf("generationConfig = %v", gc)
	}
}

func TestGeminiToOpenAI(t *testing.T) {
	body := []byte(`{
		"candidates":[{"content":{"parts":[{"text":"hi there"}]},"finishReason":"STOP"}],
		"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5}
	}`)
	out, err := geminiToOpenAI(body, "gemini-1.5-pro")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["object"] != "chat.completion" {
		t.Fatalf("object = %v", doc["object"])
	}
	choices := doc["choices"].([]any)
	ch := choices[0].(map[string]any)
	msg := ch["message"].(map[string]any)
	if msg["content"] != "hi there" {
		t.Fatalf("content = %v", msg["content"])
	}
	if ch["finish_reason"] != "stop" {
		t.Fatalf("finish_reason = %v", ch["finish_reason"])
	}
	usage := doc["usage"].(map[string]any)
	if usage["prompt_tokens"] != float64(10) || usage["completion_tokens"] != float64(5) {
		t.Fatalf("usage = %v", usage)
	}
}

func TestGeminiToOpenAIToolCall(t *testing.T) {
	body := []byte(`{
		"candidates":[{"content":{"parts":[{"functionCall":{"name":"bash","args":{"command":"ls"}}}]},"finishReason":"STOP"}]
	}`)
	out, err := geminiToOpenAI(body, "m")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	_ = json.Unmarshal(out, &doc)
	choices := doc["choices"].([]any)
	ch := choices[0].(map[string]any)
	msg := ch["message"].(map[string]any)
	tcs := msg["tool_calls"].([]any)
	if len(tcs) != 1 {
		t.Fatalf("tool_calls len = %d", len(tcs))
	}
}

func TestG2OStreamTranslate(t *testing.T) {
	var st g2oState
	// Text frame.
	out, err := st.translate(sseEvent{data: []string{`{"candidates":[{"content":{"parts":[{"text":"hi"}]}}]}`}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"delta":{"content":"hi"}`) {
		t.Fatalf("missing text delta: %s", out)
	}
	// Final frame with finishReason.
	out2, err := st.translate(sseEvent{data: []string{`{"candidates":[{"content":{"parts":[]},"finishReason":"STOP"}]}`}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out2, `"finish_reason":"stop"`) {
		t.Fatalf("missing finish_reason: %s", out2)
	}
	// Empty data frame is skipped.
	out3, err := st.translate(sseEvent{data: []string{`{}`}})
	if err != nil || out3 != "" {
		t.Fatalf("empty frame should be skipped, got %q err=%v", out3, err)
	}
}

func TestGeminiFinishMapping(t *testing.T) {
	cases := map[string]string{
		"STOP": "stop", "MAX_TOKENS": "length", "SAFETY": "content_filter", "": "stop",
	}
	for in, want := range cases {
		if got := geminiFinishToOpenAI(in); got != want {
			t.Errorf("geminiFinishToOpenAI(%q) = %q, want %q", in, got, want)
		}
	}
}
