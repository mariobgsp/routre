package dialect

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Anthropic-dialect client <-> Gemini-kind upstream.
//
// Request translation (anthropicToGemini), non-streaming response
// translation (geminiToAnthropic) and the g2a SSE state machine follow the
// same lossy-mapping contract as the OpenAI↔Gemini pair: image blocks are
// omitted with a placeholder, system prompts collapse into
// systemInstruction, and tool_use ids are fabricated deterministically
// ("toolu_<name>") because Gemini does not echo ids. The id round-trips
// through the client loop unchanged, matching the cross-kind contract in
// stream_translate.go.

// anthropicToGemini maps an Anthropic /v1/messages request to a Gemini
// generateContent body. The model is kept in the body (the relay builds the
// per-model URL path from it).
func anthropicToGemini(body []byte) ([]byte, error) {
	var in struct {
		Model       string          `json:"model"`
		MaxTokens   int             `json:"max_tokens"`
		Temperature *float64        `json:"temperature"`
		System      json.RawMessage `json:"system"`
		Messages    []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Tools []struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			InputSchema map[string]any `json:"input_schema"`
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
	toolNames := map[string]string{} // tool_use id -> name (for functionResponse)
	for _, m := range in.Messages {
		blocks := anthropicBlocks(m.Content)
		parts := []map[string]any{}
		switch m.Role {
		case "assistant":
			for _, b := range blocks {
				if b.Type == "text" && b.Text != "" {
					parts = append(parts, map[string]any{"text": b.Text})
				}
				if b.Type == "tool_use" {
					toolNames[b.ID] = b.Name
					var args any
					_ = json.Unmarshal(b.Input, &args)
					parts = append(parts, map[string]any{
						"functionCall": map[string]any{"name": b.Name, "args": args},
					})
				}
			}
		default: // user (and anything else maps to user side)
			for _, b := range blocks {
				switch b.Type {
				case "text":
					if b.Text != "" {
						parts = append(parts, map[string]any{"text": b.Text})
					}
				case "tool_result":
					name := toolNames[b.ToolUseID]
					if name == "" {
						name = "tool"
					}
					parts = append(parts, map[string]any{
						"functionResponse": map[string]any{
							"name":     name,
							"response": map[string]any{"output": toolResultText(b.Content)},
						},
					})
				default:
					parts = append(parts, map[string]any{"text": "[content omitted by routre]"})
				}
			}
		}
		if len(parts) > 0 {
			contents = append(contents, map[string]any{"role": "user", "parts": parts})
		}
	}
	// Gemini requires alternating user/model roles starting with user;
	// consecutive assistant/user turns are merged by relabeling is not
	// possible, so drop empty turns and let the API tolerate the sequence.
	if len(contents) > 0 {
		doc["contents"] = contents
	}
	if sys := strings.TrimSpace(blocksText(anthropicBlocks(in.System))); sys != "" {
		doc["systemInstruction"] = map[string]any{
			"parts": []map[string]any{{"text": sys}},
		}
	}
	if len(in.Tools) > 0 {
		decls := make([]map[string]any, 0, len(in.Tools))
		for _, t := range in.Tools {
			d := map[string]any{"name": t.Name}
			if t.Description != "" {
				d["description"] = t.Description
			}
			if len(t.InputSchema) > 0 {
				d["parameters"] = t.InputSchema
			}
			decls = append(decls, d)
		}
		doc["tools"] = []map[string]any{{"functionDeclarations": decls}}
	}
	return json.Marshal(doc)
}

// anthropicBlock is one entry of an Anthropic content block array (or the
// whole content when it is a plain string).
type anthropicBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
}

// anthropicBlocks decodes an Anthropic content field (string or block array)
// into blocks; a plain string becomes one text block.
func anthropicBlocks(raw json.RawMessage) []anthropicBlock {
	if len(raw) == 0 {
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []anthropicBlock{{Type: "text", Text: s}}
	}
	var blocks []anthropicBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil
	}
	return blocks
}

// blocksText joins the text of all text blocks.
func blocksText(blocks []anthropicBlock) string {
	var b strings.Builder
	for _, blk := range blocks {
		if blk.Type == "text" {
			b.WriteString(blk.Text)
		}
	}
	return b.String()
}

// toolResultText flattens a tool_result content (string or block array) to text.
func toolResultText(raw json.RawMessage) string {
	blocks := anthropicBlocks(raw)
	var b strings.Builder
	for _, blk := range blocks {
		if blk.Type == "text" {
			b.WriteString(blk.Text)
		}
	}
	return b.String()
}

// geminiToAnthropic maps a non-streaming Gemini generateContent response to
// an Anthropic /v1/messages response.
func geminiToAnthropic(body []byte, model string) ([]byte, error) {
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
	content := []map[string]any{}
	sawTool := false
	for _, p := range cand.Content.Parts {
		if p.FunctionCall.Name != "" {
			sawTool = true
			content = append(content, map[string]any{
				"type":  "tool_use",
				"id":    "toolu_" + p.FunctionCall.Name,
				"name":  p.FunctionCall.Name,
				"input": p.FunctionCall.Args,
			})
		} else if p.Text != "" {
			content = append(content, map[string]any{"type": "text", "text": p.Text})
		}
	}
	out := map[string]any{
		"id":            "msg_gemini",
		"type":          "message",
		"role":          "assistant",
		"model":         model,
		"content":       content,
		"stop_reason":   geminiStopToAnthropic(cand.FinishReason, sawTool),
		"stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":  in.UsageMetadata.PromptTokenCount,
			"output_tokens": in.UsageMetadata.CandidatesTokenCount,
		},
	}
	return json.Marshal(out)
}

// geminiStopToAnthropic maps a Gemini finishReason to an Anthropic
// stop_reason. Safety-family reasons lose their cause (documented loss):
// Anthropic's refusal reason is not emitted because the mapping is not
// observable by the caller anyway.
func geminiStopToAnthropic(fr string, sawTool bool) string {
	switch fr {
	case "MAX_TOKENS":
		return "max_tokens"
	case "STOP":
		if sawTool {
			return "tool_use"
		}
		return "end_turn"
	default:
		return "end_turn"
	}
}

// g2aState translates a Gemini streamGenerateContent SSE stream to Anthropic
// message SSE (message_start, content_block_start/delta/stop, message_delta,
// message_stop). Gemini frames carry whole parts (no token-level deltas), so
// each part becomes one delta event. Termination is emitted on the frame
// carrying finishReason; finish() covers abnormal EOF without one.
type g2aState struct {
	model        string
	started      bool
	blockCounter int
	openBlockIdx int // -1 when no block is open
	openIsTool   bool
	sawTool      bool
	finishReason string
	outputTokens int
	done         bool
}

func (s *g2aState) translate(evt sseEvent) (string, error) {
	data := evt.dataJSON()
	if data == "" || data == "{}" || s.done {
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
		ModelVersion  string `json:"modelVersion"`
		UsageMetadata struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.Unmarshal([]byte(data), &in); err != nil {
		return "", nil // skip non-JSON frames
	}
	if in.ModelVersion != "" && s.model == "" {
		s.model = in.ModelVersion
	}
	if len(in.Candidates) == 0 {
		return "", nil
	}
	if in.UsageMetadata.CandidatesTokenCount > 0 {
		s.outputTokens = in.UsageMetadata.CandidatesTokenCount
	}
	cand := in.Candidates[0]

	var out strings.Builder
	emit := func(e anthropicEvent) { out.WriteString(renderAnthropic(e)) }

	hasParts := len(cand.Content.Parts) > 0
	if !s.started && (hasParts || cand.FinishReason != "") {
		s.started = true
		emit(anthropicEvent{Type: "message_start", Message: &struct {
			ID    string `json:"id"`
			Model string `json:"model"`
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}{ID: "msg_stream", Model: s.model}})
	}

	for _, p := range cand.Content.Parts {
		if p.FunctionCall.Name != "" {
			s.sawTool = true
			s.closeBlock(&out)
			blockIdx := s.blockCounter
			s.blockCounter++
			s.openBlockIdx = blockIdx
			s.openIsTool = true
			emit(anthropicEvent{Type: "content_block_start", Index: blockIdx, ContentBlock: &struct {
				Type string `json:"type"`
				Text string `json:"text"`
				ID   string `json:"id"`
				Name string `json:"name"`
			}{Type: "tool_use", ID: "toolu_" + p.FunctionCall.Name, Name: p.FunctionCall.Name}})
			args := mustJSONStr(p.FunctionCall.Args)
			s.emitDelta(&out, emit, args)
			continue
		}
		if p.Text == "" {
			continue
		}
		if s.openIsTool {
			s.closeBlock(&out)
		}
		if s.openBlockIdx < 0 {
			blockIdx := s.blockCounter
			s.blockCounter++
			s.openBlockIdx = blockIdx
			s.openIsTool = false
			emit(anthropicEvent{Type: "content_block_start", Index: blockIdx, ContentBlock: &struct {
				Type string `json:"type"`
				Text string `json:"text"`
				ID   string `json:"id"`
				Name string `json:"name"`
			}{Type: "text"}})
		}
		s.emitDelta(&out, emit, p.Text)
	}

	if cand.FinishReason != "" {
		s.finishReason = geminiStopToAnthropic(cand.FinishReason, s.sawTool)
		out.WriteString(s.finish())
	}
	return out.String(), nil
}

// closeBlock emits content_block_stop for the open block, if any.
func (s *g2aState) closeBlock(out *strings.Builder) {
	if s.openBlockIdx < 0 {
		return
	}
	out.WriteString(renderAnthropic(anthropicEvent{Type: "content_block_stop", Index: s.openBlockIdx}))
	s.openBlockIdx = -1
}

// emitDelta emits one content_block_delta for the open block: input_json_delta
// for tool blocks, text_delta otherwise.
func (s *g2aState) emitDelta(out *strings.Builder, emit func(anthropicEvent), payload string) {
	if payload == "" || s.openBlockIdx < 0 {
		return
	}
	if s.openIsTool {
		emit(anthropicEvent{Type: "content_block_delta", Index: s.openBlockIdx, Delta: &struct {
			Type        *string `json:"type"`
			Text        *string `json:"text"`
			PartialJSON *string `json:"partial_json"`
			StopReason  *string `json:"stop_reason,omitempty"`
		}{Type: strPtr("input_json_delta"), PartialJSON: &payload}})
		return
	}
	emit(anthropicEvent{Type: "content_block_delta", Index: s.openBlockIdx, Delta: &struct {
		Type        *string `json:"type"`
		Text        *string `json:"text"`
		PartialJSON *string `json:"partial_json"`
		StopReason  *string `json:"stop_reason,omitempty"`
	}{Type: strPtr("text_delta"), Text: &payload}})
}

// finish emits content_block_stop for the open block, message_delta with the
// stop reason and output tokens, and message_stop. Idempotent.
func (s *g2aState) finish() string {
	if s.done {
		return ""
	}
	s.done = true
	var out strings.Builder
	s.closeBlock(&out)
	fr := s.finishReason
	if fr == "" {
		fr = "end_turn"
	}
	out.WriteString(renderAnthropic(anthropicEvent{
		Type: "message_delta",
		Delta: &struct {
			Type        *string `json:"type"`
			Text        *string `json:"text"`
			PartialJSON *string `json:"partial_json"`
			StopReason  *string `json:"stop_reason,omitempty"`
		}{StopReason: &fr},
		Usage: struct {
			OutputTokens int `json:"output_tokens,omitempty"`
		}{OutputTokens: s.outputTokens},
	}))
	out.WriteString(renderAnthropic(anthropicEvent{Type: "message_stop"}))
	return out.String()
}
