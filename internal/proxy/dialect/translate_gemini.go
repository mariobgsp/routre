package dialect

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Gemini dialect support for OpenAI-dialect clients.
//
// The gateway's clients speak OpenAI/Anthropic/Responses; Gemini is
// upstream-only. This file translates an OpenAI chat.completions request to
// a Gemini generateContent request, a Gemini generateContent response back
// to OpenAI chat.completions (non-streaming), and a Gemini
// streamGenerateContent SSE stream back to OpenAI chat.completion.chunk SSE
// (streaming). The reverse directions (OpenAI upstream -> Gemini client,
// Anthropic <-> Gemini) are not implemented — Anthropic-bound Gemini is
// rejected rather than silently mis-answered. See the pair matrix in SPEC.

// openAIToGemini maps an OpenAI chat request to Gemini generateContent.
// Known losses (documented): tool_choice/parallel_tool_calls are not mapped;
// image_url blocks become omission placeholders; the model is returned both
// in the body (for the URL path) and for informational use.
func openAIToGemini(body []byte) ([]byte, error) {
	var in struct {
		Model       string   `json:"model"`
		MaxTokens   int      `json:"max_tokens"`
		Temperature *float64 `json:"temperature"`
		Stream      bool     `json:"stream"`
		Messages    []struct {
			Role      string          `json:"role"`
			Content   json.RawMessage `json:"content"`
			ToolCalls json.RawMessage `json:"tool_calls"`
		} `json:"messages"`
		Tools []struct {
			Function struct {
				Name        string         `json:"name"`
				Description string         `json:"description"`
				Parameters  map[string]any `json:"parameters"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, err
	}

	doc := map[string]any{
		"model":            in.Model,
		"generationConfig": map[string]any{},
	}
	gc := doc["generationConfig"].(map[string]any)
	if in.MaxTokens > 0 {
		gc["maxOutputTokens"] = in.MaxTokens
	}
	if in.Temperature != nil {
		gc["temperature"] = *in.Temperature
	}

	var contents []map[string]any
	var sysText []string
	for _, m := range in.Messages {
		switch m.Role {
		case "system":
			sysText = append(sysText, contentToText(m.Content))
			continue
		case "tool":
			// OpenAI tool result -> Gemini functionResponse part.
			text := contentToText(m.Content)
			parts := []map[string]any{{
				"functionResponse": map[string]any{
					"name":     "tool",
					"response": map[string]any{"output": text},
				},
			}}
			contents = append(contents, map[string]any{"role": "user", "parts": parts})
			continue
		}
		role := m.Role
		if role == "assistant" {
			role = "model"
		}
		parts := []map[string]any{}
		// Assistant tool calls -> Gemini functionCall parts.
		if len(m.ToolCalls) > 0 {
			var tcs []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			}
			if json.Unmarshal(m.ToolCalls, &tcs) == nil {
				for _, tc := range tcs {
					var args any
					_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
					parts = append(parts, map[string]any{
						"functionCall": map[string]any{
							"name": tc.Function.Name,
							"args": args,
						},
					})
				}
			}
		}
		// Text content -> Gemini text part.
		parts = append(parts, map[string]any{"text": contentToText(m.Content)})
		contents = append(contents, map[string]any{"role": role, "parts": parts})
	}
	if len(contents) > 0 {
		doc["contents"] = contents
	}
	if len(sysText) > 0 {
		doc["systemInstruction"] = map[string]any{
			"parts": []map[string]any{{"text": strings.Join(sysText, "\n")}},
		}
	}

	// Tools -> Gemini functionDeclarations.
	if len(in.Tools) > 0 {
		decls := make([]map[string]any, 0, len(in.Tools))
		for _, t := range in.Tools {
			d := map[string]any{"name": t.Function.Name}
			if t.Function.Description != "" {
				d["description"] = t.Function.Description
			}
			if len(t.Function.Parameters) > 0 {
				d["parameters"] = t.Function.Parameters
			}
			decls = append(decls, d)
		}
		doc["tools"] = []map[string]any{{"functionDeclarations": decls}}
	}
	return json.Marshal(doc)
}

// geminiToOpenAI maps a non-streaming Gemini generateContent response to an
// OpenAI chat.completions response. usage is reported when Gemini provided
// usageMetadata; otherwise zero (the caller falls back to its own count).
func geminiToOpenAI(body []byte, model string) ([]byte, error) {
	var in struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text         string `json:"text"`
					FunctionCall struct {
						Name string         `json:"name"`
						Args map[string]any `json:"args"`
					} `json:"functionCall"`
				} `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		UsageMetadata struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, err
	}
	if len(in.Candidates) == 0 {
		return nil, fmt.Errorf("gemini: empty candidates in response")
	}
	cand := in.Candidates[0]
	var text strings.Builder
	var toolCalls []map[string]any
	for _, p := range cand.Content.Parts {
		text.WriteString(p.Text)
		if p.FunctionCall.Name != "" {
			toolCalls = append(toolCalls, map[string]any{
				"id":   "call_" + p.FunctionCall.Name,
				"type": "function",
				"function": map[string]any{
					"name":      p.FunctionCall.Name,
					"arguments": mustJSONStr(p.FunctionCall.Args),
				},
			})
		}
	}
	msg := map[string]any{"role": "assistant", "content": text.String()}
	if len(toolCalls) > 0 {
		msg["tool_calls"] = toolCalls
	}
	out := map[string]any{
		"id":      "chatcmpl-gemini",
		"object":  "chat.completion",
		"model":   model,
		"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": geminiFinishToOpenAI(cand.FinishReason)}},
	}
	if in.UsageMetadata.PromptTokenCount > 0 {
		out["usage"] = map[string]any{
			"prompt_tokens":     in.UsageMetadata.PromptTokenCount,
			"completion_tokens": in.UsageMetadata.CandidatesTokenCount,
			"total_tokens":      in.UsageMetadata.PromptTokenCount + in.UsageMetadata.CandidatesTokenCount,
		}
	}
	return json.Marshal(out)
}

// geminiFinishToOpenAI maps a Gemini finishReason to an OpenAI finish_reason.
func geminiFinishToOpenAI(fr string) string {
	switch fr {
	case "STOP":
		return "stop"
	case "MAX_TOKENS":
		return "length"
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII":
		return "content_filter"
	default:
		return "stop"
	}
}

// g2oState translates a Gemini streamGenerateContent SSE stream to OpenAI
// chat.completion.chunk SSE. Gemini frames carry text deltas and an optional
// final finishReason; we accumulate nothing and emit per-frame chunks.
type g2oState struct {
	emitted bool
}

// translate handles one Gemini SSE frame and returns OpenAI chunk bytes.
func (s *g2oState) translate(evt sseEvent) (string, error) {
	data := evt.dataJSON()
	if data == "" || data == "{}" {
		return "", nil
	}
	var in struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text         string `json:"text"`
					FunctionCall struct {
						Name string         `json:"name"`
						Args map[string]any `json:"args"`
					} `json:"functionCall"`
				} `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(data), &in); err != nil {
		return "", nil // skip non-JSON frames
	}
	if len(in.Candidates) == 0 {
		return "", nil
	}
	cand := in.Candidates[0]
	var delta strings.Builder
	var toolCalls []map[string]any
	for _, p := range cand.Content.Parts {
		delta.WriteString(p.Text)
		if p.FunctionCall.Name != "" {
			toolCalls = append(toolCalls, map[string]any{
				"id":   "call_" + p.FunctionCall.Name,
				"type": "function",
				"function": map[string]any{
					"name":      p.FunctionCall.Name,
					"arguments": mustJSONStr(p.FunctionCall.Args),
				},
			})
		}
	}
	var out strings.Builder
	if delta.Len() > 0 || len(toolCalls) > 0 {
		chunk := map[string]any{
			"id": "chatcmpl-gemini", "object": "chat.completion.chunk",
			"choices": []any{map[string]any{
				"index": 0, "delta": map[string]any{"content": delta.String()},
			}},
		}
		if len(toolCalls) > 0 {
			chunk["choices"] = []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": toolCalls}}}
		}
		out.WriteString("data: " + mustJSONStr(chunk) + "\n\n")
		s.emitted = true
	}
	// Final frame: emit finish_reason (or a default on the last candidate).
	if cand.FinishReason != "" {
		fr := geminiFinishToOpenAI(cand.FinishReason)
		chunk := map[string]any{
			"id": "chatcmpl-gemini", "object": "chat.completion.chunk",
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": fr}},
		}
		out.WriteString("data: " + mustJSONStr(chunk) + "\n\n")
		out.WriteString("data: [DONE]\n\n")
		s.emitted = true
	}
	return out.String(), nil
}

func mustJSONStr(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}
