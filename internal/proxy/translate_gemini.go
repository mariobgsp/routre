package proxy

import (
	"encoding/json"
	"github.com/mariobgsp/routre/internal/proxy/dialect"
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
func openAIToGemini(body []byte) ([]byte, error) { return dialect.OpenAIToGemini(body) }

// geminiToOpenAI maps a non-streaming Gemini generateContent response to an
// OpenAI chat.completions response. usage is reported when Gemini provided
// usageMetadata; otherwise zero (the caller falls back to its own count).
func geminiToOpenAI(body []byte, model string) ([]byte, error) {
	return dialect.GeminiToOpenAI(body, model)
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
