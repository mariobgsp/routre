package proxy

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// Responses API (<-> chat.completions) dialect support.
//
// opencode's stock `openai` provider speaks the OpenAI Responses API:
// it POSTs to /v1/responses with {model, instructions, input, ...} and (for
// streaming) consumes a distinct SSE event vocabulary (response.created,
// response.output_text.delta, response.completed, ...). The gateway's native
// dialect is chat.completions: every tiered provider (opencode-go,
// opencode-zen, openrouter, ...) is configured with kind "openai" and is
// reached over /v1/chat/completions. There is no provider upstream that
// natively serves /v1/responses, so the gateway must bridge the wire formats.
//
// Translation strategy (matches the existing Anthropic<->OpenAI bridging):
//   - Request  : responsesJSON -> chat request        (responsesToOpenAI)
//   - Non-streaming response: chat envelope -> responses envelope
//     (openAIToResponses)
//   - Streaming      : chat SSE -> responses SSE      (respToOpenAI stream
//     translator in stream_translate.go)
//
// Known losses (documented in SPEC.md): the many Responses-only controls
// (store, metadata, previous_response_id, parallel_tool_calls, text/format,
// reasoning effort knobs beyond max_output_tokens, ...) are dropped; tool
// calls are carried as the OpenAI function-call dialect which opencode's
// responses provider re-admits identically. This is deliberate and
// forward-compatible: any field we pass through could be rejected by an
// upstream that does not understand it, so only fields with direct chat
// equivalents are mapped.

// responsesItem is one element of a Responses `input` array.
type responsesItem struct {
	Type    string          `json:"type"`
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
	// function_call / function_call_output
	Name      string `json:"name"`
	CallID    string `json:"call_id"`
	Arguments string `json:"arguments"`
	Output    string `json:"output"`
}

// responsesContentBlock is one element of a Responses message content array.
type responsesContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// responsesToOpenAI maps a Responses API request to a chat.completions
// request, so the request can flow through the standard relay and candidate
// selection. Returns the chat body (with "messages" and "max_tokens").
func responsesToOpenAI(body []byte) ([]byte, error) {
	var in struct {
		Model               string          `json:"model"`
		Instructions        string          `json:"instructions"`
		Input               json.RawMessage `json:"input"`
		MaxOutputTokens     int             `json:"max_output_tokens"`
		Temperature         *float64        `json:"temperature"`
		Stream              bool            `json:"stream"`
		Tools               []json.RawMessage `json:"tools"`
		ToolChoice          json.RawMessage `json:"tool_choice"`
		ParallelToolCalls   *bool           `json:"parallel_tool_calls"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, err
	}

type chatToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

	type chatMsg struct {
		Role       string         `json:"role"`
		Content    any            `json:"content"`
		ToolCallID string         `json:"tool_call_id,omitempty"`
		ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	}


	var msgs []chatMsg
	if in.Instructions != "" {
		msgs = append(msgs, chatMsg{Role: "system", Content: in.Instructions})
	}

	// `input` may be a bare string or an array of items.
	if len(in.Input) > 0 {
		first := in.Input[0]
		if first == '"' {
			var s string
			if err := json.Unmarshal(in.Input, &s); err == nil {
				msgs = append(msgs, chatMsg{Role: "user", Content: s})
			}
		} else {
			var items []responsesItem
			if err := json.Unmarshal(in.Input, &items); err != nil {
				return nil, fmt.Errorf("responses: parse input: %w", err)
			}
			for _, it := range items {
				switch it.Type {
				case "message":
					role := it.Role
					if role == "" {
						role = "user"
					}
					// Flatten the content block array to a single text string
					// (image blocks omitted) or pass a plain string through.
					content := responsesContentToText(it.Content)
					msgs = append(msgs, chatMsg{Role: role, Content: content})
				case "function_call":
					msgs = append(msgs, chatMsg{
						Role:    "assistant",
						Content: "",
						ToolCalls: []chatToolCall{{
							ID:   it.CallID,
							Type: "function",
							Function: struct {
								Name      string `json:"name"`
								Arguments string `json:"arguments"`
							}{Name: it.Name, Arguments: it.Arguments},
						}},
					})
				case "function_call_output":
					msgs = append(msgs, chatMsg{Role: "tool", Content: it.Output, ToolCallID: it.CallID})
				case "reasoning":
					// Not representable in chat; drop.
				default:
					// Unknown item type; skip rather than fail the request.
				}
			}
		}
	}
	if len(msgs) == 0 {
		msgs = append(msgs, chatMsg{Role: "user", Content: ""})
	}

	doc := map[string]any{
		"model":      in.Model,
		"messages":   msgs,
		"max_tokens": maxInt(in.MaxOutputTokens, 4096),
	}

	// Tools: map Responses {type:"function", name, description, parameters,
	// strict} straight into the chat function tool shape.
	if len(in.Tools) > 0 {
		var tools []map[string]any
		for _, raw := range in.Tools {
			var t struct {
				Type        string          `json:"type"`
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
				Strict      bool            `json:"strict"`
			}
			if err := json.Unmarshal(raw, &t); err != nil {
				continue
			}
			if t.Type != "" && t.Type != "function" {
				continue
			}
			fn := map[string]any{"name": t.Name, "description": t.Description}
			if len(t.Parameters) > 0 {
				fn["parameters"] = rawMessageValue(t.Parameters)
			}
			if t.Strict {
				fn["strict"] = true
			}
			tools = append(tools, map[string]any{"type": "function", "function": fn})
		}
		if len(tools) > 0 {
			doc["tools"] = tools
		}
	}
	if len(in.ToolChoice) > 0 {
		if v := rawMessageValue(in.ToolChoice); v != nil {
			doc["tool_choice"] = v
		}
	}
	if in.Stream {
		doc["stream"] = true
	}
	if in.Temperature != nil {
		doc["temperature"] = *in.Temperature
	}
	if in.ParallelToolCalls != nil {
		doc["parallel_tool_calls"] = *in.ParallelToolCalls
	}
	return json.Marshal(doc)
}

// responsesContentToText flattens a Responses content array (or plain
// string) to chat text. Image input is dropped (matching cross-kind losses).
func responsesContentToText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
		return ""
	}
	var blocks []responsesContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	var text string
	for _, b := range blocks {
		switch b.Type {
		case "input_text", "output_text", "text":
			text += b.Text
		default:
			// input_image, refusal, ... omitted.
		}
	}
	return text
}

// rawMessageValue decodes a json.RawMessage into a plain Go value, or nil
// when it is not valid JSON.
func rawMessageValue(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	return v
}

// openAIToResponses wraps a chat.completions non-streaming response in the
// Responses envelope opencode's SDK parses: {id, object:"response",
// created_at, status, model, output:[...], usage}.
func openAIToResponses(chatBody []byte, model string) ([]byte, error) {
	now := time.Now().Unix()
	id := "resp_" + strconv.FormatInt(now, 10)

	var in struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		Model   string `json:"model"`
		Choices []struct {
			Index        int `json:"index"`
			Message      struct {
				Role      string `json:"role"`
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(chatBody, &in); err != nil {
		return nil, err
	}

	if in.Model == "" {
		in.Model = model
	}

	var output []map[string]any
	for _, c := range in.Choices {
		// Tool calls become their own top-level output items; text becomes a
		// message output item with one output_text part.
		var toolCalls []map[string]any
		for _, tc := range c.Message.ToolCalls {
			toolCalls = append(toolCalls, map[string]any{
				"type":      "function_call",
				"id":        "fc_" + tc.ID,
				"call_id":   tc.ID,
				"name":      tc.Function.Name,
				"arguments": tc.Function.Arguments,
				"status":    "completed",
			})
		}
		var content []map[string]any
		if c.Message.Content != "" {
			content = append(content, map[string]any{
				"type":        "output_text",
				"text":        c.Message.Content,
				"annotations": []any{},
			})
		}
		if len(content) > 0 || len(toolCalls) == 0 {
			output = append(output, map[string]any{
				"type":    "message",
				"id":      "msg_" + strconv.FormatInt(now, 10) + "_" + strconv.Itoa(c.Index),
				"status":  "completed",
				"role":    c.Message.Role,
				"content": content,
			})
		}
		output = append(output, toolCalls...)
	}

	status := "completed"
	// finish_reason maps to response status for the SDK.
	switch {
	case len(in.Choices) > 0 && in.Choices[0].FinishReason == "length":
		status = "incomplete"
	}

	usage := map[string]any{
		"input_tokens":  in.Usage.PromptTokens,
		"output_tokens": in.Usage.CompletionTokens,
		"total_tokens":  in.Usage.TotalTokens,
	}

	doc := map[string]any{
		"id":         id,
		"object":     "response",
		"created_at": now,
		"status":     status,
		"model":      in.Model,
		"output":     output,
		"usage":      usage,
	}
	return json.Marshal(doc)
}

// renderResponsesEvent renders an SSE frame with the given Responses event
// name (unlike chat.completions, Responses SSE carries named events).
func renderResponsesEvent(event string, data any) string {
	return sseFrame(event, mustMarshal(data))
}
