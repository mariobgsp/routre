package dialect

import (
	"encoding/json"
	"testing"
)

// TestOpenAIToAnthropicCacheControl verifies the OpenAI->Anthropic translation
// injects cache_control: {type: "ephemeral"} on the system block and on the
// last two messages, so every request that lands on an Anthropic provider
// benefits from the upstream prompt cache. Without explicit breakpoints,
// Anthropic's implicit cache only covers one prefix per conversation; the
// explicit pair captures the system prompt (the largest stable prefix in
// agent traffic) plus the conversation tail.
func TestOpenAIToAnthropicCacheControl(t *testing.T) {
	body := []byte(`{
		"model":"claude-opus-4-5",
		"max_tokens":1024,
		"system":"you are a careful assistant",
		"messages":[
			{"role":"user","content":"first turn"},
			{"role":"assistant","content":"first reply"},
			{"role":"user","content":"second turn"},
			{"role":"assistant","content":"second reply"},
			{"role":"user","content":"third turn"}
		]
	}`)
	out, err := openAItoAnthropic(body)
	if err != nil {
		t.Fatalf("openAItoAnthropic: %v", err)
	}
	var doc struct {
		System []map[string]any `json:"system"`
		// Messages use a content field that may be a string or block array.
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, out)
	}
	// System: must be a content-block array with cache_control on the only block.
	if len(doc.System) != 1 {
		t.Fatalf("system: want 1 block, got %d (out=%s)", len(doc.System), out)
	}
	cc, ok := doc.System[0]["cache_control"]
	if !ok {
		t.Fatalf("system block missing cache_control: %+v", doc.System[0])
	}
	if ccm, ok := cc.(map[string]any); !ok || ccm["type"] != "ephemeral" {
		t.Fatalf("system cache_control wrong: %+v", cc)
	}
	if doc.System[0]["text"] != "you are a careful assistant" {
		t.Fatalf("system text lost: %+v", doc.System[0])
	}
	// Messages: only the last 2 carry cache_control; the earlier 3 do not.
	if len(doc.Messages) != 5 {
		t.Fatalf("want 5 messages, got %d", len(doc.Messages))
	}
	for i, m := range doc.Messages {
		// Messages in the last 2 have their string content promoted to a
		// single text block (so cache_control can attach); earlier messages
		// keep their original string content.
		var last map[string]any
		switch c := m["content"].(type) {
		case string:
			last = map[string]any{"text": c}
		case []any:
			if len(c) == 0 {
				t.Fatalf("msg[%d] empty content array", i)
			}
			last = c[len(c)-1].(map[string]any)
		default:
			t.Fatalf("msg[%d] content unexpected type %T: %+v", i, m["content"], m)
		}
		_, hasCC := last["cache_control"]
		wantCC := i >= len(doc.Messages)-2
		if hasCC != wantCC {
			t.Fatalf("msg[%d] cache_control: has=%v want=%v (msg=%+v)", i, hasCC, wantCC, m)
		}
		if wantCC {
			if ccm, ok := last["cache_control"].(map[string]any); !ok || ccm["type"] != "ephemeral" {
				t.Fatalf("msg[%d] cache_control wrong: %+v", i, last["cache_control"])
			}
		}
	}
}

// TestOpenAIToAnthropicCacheControlSingleMessage covers the edge case where
// the conversation has only one message: the last message is still marked
// (it's the tail of a 1-message conversation) and the system block is too.
func TestOpenAIToAnthropicCacheControlSingleMessage(t *testing.T) {
	body := []byte(`{
		"model":"claude-opus-4-5",
		"max_tokens":1024,
		"system":"be brief",
		"messages":[{"role":"user","content":"hi"}]
	}`)
	out, err := openAItoAnthropic(body)
	if err != nil {
		t.Fatalf("openAItoAnthropic: %v", err)
	}
	var doc struct {
		System   []map[string]any `json:"system"`
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, out)
	}
	if len(doc.System) != 1 || doc.System[0]["cache_control"] == nil {
		t.Fatalf("system cache_control missing: %+v", doc.System)
	}
	if len(doc.Messages) != 1 {
		t.Fatalf("want 1 message, got %d", len(doc.Messages))
	}
	// Single-message case: content is a single-message promoted block array.
	blocks := doc.Messages[0]["content"].([]any)
	last := blocks[len(blocks)-1].(map[string]any)
	if last["cache_control"] == nil {
		t.Fatalf("single message must still be marked: %+v", last)
	}
}

// TestOpenAIToAnthropicNoSystemNoBreakpoints verifies that without a
// system field, the request still has cache_control on the last two
// messages. The system breakpoint is not required for the cache to work;
// the conversation-tail breakpoints alone are enough.
func TestOpenAIToAnthropicNoSystemNoBreakpoints(t *testing.T) {
	body := []byte(`{
		"model":"claude-opus-4-5",
		"max_tokens":1024,
		"messages":[
			{"role":"user","content":"a"},
			{"role":"assistant","content":"b"},
			{"role":"user","content":"c"}
		]
	}`)
	out, err := openAItoAnthropic(body)
	if err != nil {
		t.Fatalf("openAItoAnthropic: %v", err)
	}
	var doc struct {
		System   any              `json:"system"`
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, out)
	}
	if doc.System != nil {
		t.Fatalf("system must be absent (nil) when input had no system: %+v", doc.System)
	}
	if len(doc.Messages) != 3 {
		t.Fatalf("want 3 messages, got %d", len(doc.Messages))
	}
	// Only the last 2 are marked. Earlier messages keep their string content
	// (no promotion needed since they are not in the cache tail).
	for i, m := range doc.Messages {
		var last map[string]any
		switch c := m["content"].(type) {
		case string:
			last = map[string]any{"text": c}
		case []any:
			last = c[len(c)-1].(map[string]any)
		default:
			t.Fatalf("msg[%d] unexpected content type %T", i, m["content"])
		}
		_, hasCC := last["cache_control"]
		wantCC := i >= 1
		if hasCC != wantCC {
			t.Fatalf("msg[%d] has=%v want=%v", i, hasCC, wantCC)
		}
	}
}
