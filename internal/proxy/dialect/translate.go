package dialect

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// translateBody converts a request between the OpenAI and Anthropic dialects
// for cross-kind provider routing. It is intentionally minimal and lossy:
// full fidelity translation is out of scope for the first cut. Streaming
// cross-kind translation is rejected upstream (501).
func translateBody(from, to Format, body []byte) ([]byte, error) {
	switch {
	case from == FormatOpenAI && to == FormatAnthropic:
		return openAItoAnthropic(body)
	case from == FormatAnthropic && to == FormatOpenAI:
		return anthropicToOpenAI(body)
	case from == FormatOpenAI && to == FormatGemini:
		return openAIToGemini(body)
	default:
		return nil, fmt.Errorf("unsupported translation %v -> %v", from, to)
	}
}

// openAItoAnthropic maps an OpenAI chat request to Anthropic /v1/messages.
// Known losses (documented in SPEC.md):
//   - tools: not mapped (tool definitions are dropped);
//   - image_url blocks: replaced with an omission placeholder;
//   - function calls / tool_calls: flattened into text.
func openAItoAnthropic(body []byte) ([]byte, error) {
	var in struct {
		Model       string          `json:"model"`
		MaxTokens   int             `json:"max_tokens"`
		Temperature *float64        `json:"temperature"`
		Stream      bool            `json:"stream"`
		System      json.RawMessage `json:"system"`
		Messages    []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
			Name    string          `json:"name"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, err
	}

	var systemParts []string
	if len(in.System) > 0 {
		var s string
		if json.Unmarshal(in.System, &s) == nil {
			systemParts = append(systemParts, s)
		}
	}

	type outMsg struct {
		Role    string `json:"role"`
		Content any    `json:"content"`
	}
	var out []outMsg
	for _, m := range in.Messages {
		switch m.Role {
		case "system":
			if s, ok := contentAsString(m.Content); ok {
				systemParts = append(systemParts, s)
			}
			continue
		case "tool":
			s, ok := contentAsString(m.Content)
			if !ok {
				s = string(m.Content)
			}
			out = append(out, outMsg{Role: "user", Content: []map[string]any{
				{"type": "tool_result", "content": s},
			}})
			continue
		}
		text := contentToText(m.Content)
		out = append(out, outMsg{Role: m.Role, Content: text})
	}

	doc := map[string]any{
		"model":      in.Model,
		"max_tokens": maxInt(in.MaxTokens, 4096),
		"messages":   out,
	}
	if len(systemParts) > 0 {
		doc["system"] = joinStrings(systemParts, "\n")
	}
	if in.Stream {
		doc["stream"] = true
	}
	if in.Temperature != nil {
		doc["temperature"] = *in.Temperature
	}
	return json.Marshal(doc)
}

// anthropicToOpenAI maps an Anthropic /v1/messages request to OpenAI chat.
// Known losses (documented in SPEC.md):
//   - tool_use blocks are flattened into text (OpenAI chat has no tool_use
//     block type);
//   - tool_result blocks lose the tool_use_id linkage;
//   - tools definitions are dropped.
func anthropicToOpenAI(body []byte) ([]byte, error) {
	var in struct {
		Model       string   `json:"model"`
		MaxTokens   int      `json:"max_tokens"`
		Temperature *float64 `json:"temperature"`
		Stream      bool     `json:"stream"`
		System      string   `json:"system"`
		Messages    []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, err
	}

	type outMsg struct {
		Role    string `json:"role"`
		Content any    `json:"content"`
	}
	var out []outMsg
	if in.System != "" {
		out = append(out, outMsg{Role: "system", Content: in.System})
	}
	for _, m := range in.Messages {
		switch c := m.Content.(type) {
		case string:
			out = append(out, outMsg{Role: m.Role, Content: c})
		case []any:
			var parts []map[string]any
			for _, blk := range c {
				b, ok := blk.(map[string]any)
				if !ok {
					continue
				}
				switch b["type"] {
				case "text":
					if t, ok := b["text"].(string); ok {
						parts = append(parts, map[string]any{"type": "text", "text": t})
					}
				case "tool_result":
					// Flatten; the tool_call_id linkage is lost (documented).
					if s, ok := b["content"].(string); ok {
						parts = append(parts, map[string]any{"type": "text", "text": "[tool_result] " + s})
					} else if arr, ok := b["content"].([]any); ok {
						var sb bytes.Buffer
						sb.WriteString("[tool_result] ")
						for _, tb := range arr {
							if tbm, ok := tb.(map[string]any); ok {
								if t, ok := tbm["text"].(string); ok {
									sb.WriteString(t)
								}
							}
						}
						parts = append(parts, map[string]any{"type": "text", "text": sb.String()})
					}
				case "tool_use":
					if id, _ := b["id"].(string); id != "" {
						if name, _ := b["name"].(string); name != "" {
							input, _ := json.Marshal(b["input"])
							parts = append(parts, map[string]any{
								"type": "text",
								"text": fmt.Sprintf("[tool_use id=%s name=%s input=%s]", id, name, string(input)),
							})
						}
					}
				}
			}
			out = append(out, outMsg{Role: m.Role, Content: parts})
		default:
			out = append(out, outMsg{Role: m.Role, Content: string(mustRaw(c))})
		}
	}

	doc := map[string]any{
		"model":      in.Model,
		"max_tokens": maxInt(in.MaxTokens, 4096),
		"messages":   out,
	}
	if in.Stream {
		doc["stream"] = true
	}
	if in.Temperature != nil {
		doc["temperature"] = *in.Temperature
	}
	return json.Marshal(doc)
}

// contentAsString returns the content field if it is a plain string.
// AnthropicToOpenAIResponse maps a non-streaming Anthropic /v1/messages response to OpenAI chat.
// It is the minimal response analog of anthropicToOpenAI (request). It extracts the text content
// and maps stop_reason to finish_reason, preserving usage when present.
func AnthropicToOpenAIResponse(body []byte) ([]byte, error) {
	var in struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, err
	}
	text := ""
	var toolCalls []map[string]any
	for _, c := range in.Content {
		switch c.Type {
		case "text":
			text += c.Text
		case "tool_use":
			toolCalls = append(toolCalls, map[string]any{
				"id":   c.ID,
				"type": "function",
				"function": map[string]any{
					"name":      c.Name,
					"arguments": string(c.Input),
				},
			})
		}
	}
	fr := "stop"
	switch in.StopReason {
	case "max_tokens":
		fr = "length"
	case "tool_use":
		fr = "tool_calls"
	case "content_filter":
		fr = "content_filter"
	}
	msg := map[string]any{"role": "assistant", "content": text}
	if len(toolCalls) > 0 {
		msg["tool_calls"] = toolCalls
	}
	out := map[string]any{
		"id":     in.ID,
		"object": "chat.completion",
		"model":  in.Model,
		"choices": []any{map[string]any{
			"index": 0, "message": msg, "finish_reason": fr,
		}},
		"usage": map[string]any{
			"prompt_tokens":     in.Usage.InputTokens,
			"completion_tokens": in.Usage.OutputTokens,
			"total_tokens":      in.Usage.InputTokens + in.Usage.OutputTokens,
		},
	}
	return json.Marshal(out)
}

func contentAsString(raw json.RawMessage) (string, bool) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, true
	}
	return "", false
}

// contentToText flattens a content field (string or block array) to text,
// replacing image blocks with a placeholder.
func contentToText(raw json.RawMessage) string {
	if s, ok := contentAsString(raw); ok {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var b bytes.Buffer
		for _, blk := range blocks {
			switch blk.Type {
			case "text":
				b.WriteString(blk.Text)
			case "image_url":
				b.WriteString("[image omitted by routre]")
			default:
				b.WriteString("[content block omitted by routre]")
			}
		}
		return b.String()
	}
	return "[content omitted by routre]"
}

func mustRaw(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func joinStrings(parts []string, sep string) string {
	var b bytes.Buffer
	for i, p := range parts {
		if i > 0 {
			b.WriteString(sep)
		}
		b.WriteString(p)
	}
	return b.String()
}
