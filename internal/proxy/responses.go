package proxy

import (
	"encoding/json"

	"github.com/mariobgsp/routre/internal/proxy/dialect"
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
func responsesToOpenAI(body []byte) ([]byte, error) { return dialect.ResponsesToOpenAI(body) }

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
func openAIToResponses(body []byte, model string) ([]byte, error) {
	return dialect.OpenAIToResponses(body, model)
}

// renderResponsesEvent renders an SSE frame with the given Responses event
// name (unlike chat.completions, Responses SSE carries named events).
func renderResponsesEvent(event string, data any) string {
	return sseFrame(event, mustMarshal(data))
}
