package proxy

import (
	"bytes"
	"encoding/json"
)

// injectPromptCache marks Anthropic cache breakpoints on an outbound
// /v1/messages body: the system prefix and the last message's final text
// block get cache_control {type:"ephemeral"}. It is strictly additive and
// fail-open — an already-present cache_control is never overwritten, and
// malformed input is returned unchanged. When the last message's content is
// a plain string (no block array), cache_control cannot be attached there,
// so only the system block is marked.
func injectPromptCache(body []byte) []byte {
	if !json.Valid(body) {
		return body
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return body
	}
	changed := false

	// Mark the system prefix: if an array, attach to the first text block;
	// if a plain string, wrap it into an array so the breakpoint is
	// expressible.
	if sys, ok := doc["system"]; ok {
		switch s := sys.(type) {
		case []any:
			changed = markTextBlock(s, true) || changed
		case string:
			if s != "" {
				doc["system"] = []any{map[string]any{
					"type":          "text",
					"text":          s,
					"cache_control": map[string]any{"type": "ephemeral"},
				}}
				changed = true
			}
		}
	}

	// Mark the last message's content if it is a block array with a
	// markable text block.
	if msgs, ok := doc["messages"].([]any); ok && len(msgs) > 0 {
		if last, ok := msgs[len(msgs)-1].(map[string]any); ok {
			if content, ok := last["content"].([]any); ok {
				changed = markTextBlock(content, false) || changed
			}
		}
	}

	if !changed {
		return body
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return body
	}
	return out
}

// markTextBlock attaches ephemeral cache_control to the first (first=true,
// system prefix) or last (rolling agentic context) text block.
// Never overwrites an existing breakpoint.
func markTextBlock(blocks []any, first bool) bool {
	idx := func(i int) int {
		if first {
			return i
		}
		return len(blocks) - 1 - i
	}
	for i := 0; i < len(blocks); i++ {
		bm, ok := blocks[idx(i)].(map[string]any)
		if !ok {
			continue
		}
		if t, _ := bm["type"].(string); t != "text" {
			continue
		}
		if _, already := bm["cache_control"]; already {
			return false
		}
		bm["cache_control"] = map[string]any{"type": "ephemeral"}
		return true
	}
	return false
}
