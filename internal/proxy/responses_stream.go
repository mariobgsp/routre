package proxy

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Chat.completions SSE -> Responses SSE streaming translator.
//
// This bridges the final hop for a streaming opencode Responses request whose
// upstream provider answered over /v1/chat/completions. It consumes the
// standard chat.completion.chunk frame stream and re-emits the named
// Responses events opencode's @ai-sdk/openai responses provider parses:
//
//	response.created
//	response.in_progress
//	response.output_item.added   (message when text, function_call when a tool)
//	response.content_part.added
//	response.output_text.delta   (per text fragment)
//	response.output_text.done
//	response.content_part.done
//	response.output_item.done
//	response.function_call_arguments.delta / .done
//	response.completed           (carries the assembled response + usage)
//	data: [DONE]
//
// It reuses the same failover contract as the other stream translators:
// nothing is emitted to the client before the first parseable chunk, and a
// translation error before that first byte is retryable.
//
// The translator emits a single message item (when the assistant streams
// text) plus one function_call item per tool, mirroring how opencode's
// provider re-admits a chat response, keeping the standard agentic flow
// lossless. Output items are tracked in one ordered slice so every emitted
// output_index matches the item's position in the final output array.

// r2oItem is one assembled Responses output item (message or function_call).
type r2oItem struct {
	typ   string // "message" | "function_call"
	id    string
	open  bool // the item is still streaming (used by finalize to close)
	index int
	// message
	text strings.Builder
	// function_call
	callID string
	name   string
	args   strings.Builder
}

// r2oState is the chat->responses per-stream state.
type r2oState struct {
	respID    string
	model     string
	createdAt int64
	started   bool
	items     []*r2oItem
	openText  bool // whether a message text item is the currently-open one
	status    string
	done      bool
	// usage carried from the final chat chunk.
	prompt, completion int64
}

// translate processes one chat.completion.chunk and returns Responses SSE.
func (s *r2oState) translate(evt sseEvent) (string, error) {
	data := evt.dataJSON()
	if data == "" {
		return "", nil
	}
	if data == "[DONE]" {
		return s.finalize(nil), nil
	}

	var m struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Model   string `json:"model"`
		Created int64  `json:"created"`
		Usage   *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
		Choices []struct {
			Index int `json:"index"`
			Delta struct {
				Role      string  `json:"role"`
				Content   *string `json:"content"`
				ToolCalls []struct {
					Index    int    `json:"index"`
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := unmarshalSSE(data, &m); err != nil {
		return "", fmt.Errorf("parse chat chunk %q: %w", data, err)
	}

	if m.Usage != nil {
		s.prompt = int64(m.Usage.PromptTokens)
		s.completion = int64(m.Usage.CompletionTokens)
	}

	var out string

	if !s.started {
		s.started = true
		s.respID = "resp_" + strconv.FormatInt(time.Now().Unix(), 10)
		if m.ID != "" {
			s.respID = "resp_" + m.ID
		}
		s.model = m.Model
		s.createdAt = time.Now().Unix()
		if m.Created != 0 {
			s.createdAt = m.Created
		}
		out += s.renderCreated()
		out += renderResponsesEvent("response.in_progress", map[string]any{"type": "response.in_progress"})
	}

	c := m.Choices[0]

	// Tool-call fragments -> function_call items. Identify fragments by the
	// index field rather than by opening on every chunk: the first fragment
	// for an index carries id/name; subsequent fragments carry only args.
	for _, tc := range c.Delta.ToolCalls {
		idx := tc.Index
		item := s.toolAt(idx)
		if item == nil {
			// New tool index: open a function_call item.
			id := tc.ID
			if id == "" {
				id = "call_" + strconv.Itoa(idx)
			}
			item = &r2oItem{typ: "function_call", id: "fc_" + id, callID: id, name: tc.Function.Name, open: true, index: len(s.items)}
			s.items = append(s.items, item)
			s.openText = false
			out += s.renderItemAdded(item)
		}
		if tc.Function.Arguments != "" {
			item.args.WriteString(tc.Function.Arguments)
			out += s.renderToolArgsDelta(item)
		}
	}

	// Text deltas -> message item.
	if c.Delta.Content != nil && *c.Delta.Content != "" {
		if !s.openText {
			// Close any open tool-call item and open the message item. The
			// Responses item ORDER matches chat's: if the assistant produced
			// text after tools, that is a new message output item appended
			// after the tool items.
			s.openText = true
			msg := &r2oItem{typ: "message", id: "msg_" + strconv.Itoa(len(s.items)), open: true, index: len(s.items)}
			s.items = append(s.items, msg)
			out += s.renderItemAdded(msg)
			out += s.renderContentPartAdded(msg)
		}
		msg := s.items[len(s.items)-1]
		msg.text.WriteString(*c.Delta.Content)
		out += s.renderTextDelta(msg, *c.Delta.Content)
	}

	if c.FinishReason != nil && *c.FinishReason != "" {
		out += s.finalize(c.FinishReason)
	}
	return out, nil
}

// toolAt returns the open function_call item for a chat tool index, or nil.
func (s *r2oState) toolAt(chatIdx int) *r2oItem {
	var found *r2oItem
	n := 0
	for _, it := range s.items {
		if it.typ == "function_call" {
			if n == chatIdx {
				found = it
			}
			n++
		}
	}
	return found
}

func (s *r2oState) renderCreated() string {
	return renderResponsesEvent("response.created", map[string]any{
		"type": "response.created",
		"response": map[string]any{
			"id":         s.respID,
			"object":     "response",
			"created_at": s.createdAt,
			"status":     "in_progress",
			"model":      s.model,
			"output":     []any{},
		},
	})
}

func (s *r2oState) renderItemAdded(it *r2oItem) string {
	return renderResponsesEvent("response.output_item.added", map[string]any{
		"type":         "response.output_item.added",
		"output_index": it.index,
		"item":         it.partialItem(),
	})
}

func (s *r2oState) renderContentPartAdded(it *r2oItem) string {
	return renderResponsesEvent("response.content_part.added", map[string]any{
		"type":          "response.content_part.added",
		"item_id":       it.id,
		"output_index":  it.index,
		"content_index": 0,
		"part":          it.textPart(),
	})
}

func (s *r2oState) renderTextDelta(it *r2oItem, fragment string) string {
	return renderResponsesEvent("response.output_text.delta", map[string]any{
		"type":           "response.output_text.delta",
		"item_id":        it.id,
		"output_index":   it.index,
		"content_index":  0,
		"delta":          fragment,
	})
}

func (s *r2oState) renderToolArgsDelta(it *r2oItem) string {
	return renderResponsesEvent("response.function_call_arguments.delta", map[string]any{
		"type":         "response.function_call_arguments.delta",
		"item_id":      it.id,
		"output_index": it.index,
		"delta":        it.args.String(),
	})
}

// partialItem renders the in-progress shape of an output item.
func (it *r2oItem) partialItem() map[string]any {
	if it.typ == "function_call" {
		return map[string]any{
			"type":      "function_call",
			"id":        it.id,
			"call_id":   it.callID,
			"name":      it.name,
			"arguments": it.args.String(),
			"status":    "in_progress",
		}
	}
	return map[string]any{
		"type":    "message",
		"id":      it.id,
		"status":  "in_progress",
		"role":    "assistant",
		"content": []any{},
	}
}

// textPart renders the output_text part of a message item.
func (it *r2oItem) textPart() map[string]any {
	return map[string]any{"type": "output_text", "text": it.text.String(), "annotations": []any{}}
}

// finalize closes all open items and emits response.completed + [DONE].
// Idempotent; finishReason is optional.
func (s *r2oState) finalize(finishReason *string) string {
	if s.done {
		return ""
	}
	s.done = true

	status := "completed"
	if finishReason != nil && *finishReason == "length" {
		status = "incomplete"
	}

	var out string

	// Assemble the final output array.
	var output []any
	for _, it := range s.items {
		it.open = false
		output = append(output, it.completedItem())
	}

	// Close event per item: text items get output_text.done + content_part.done
	// + output_item.done; function calls get args.done + output_item.done.
	for _, it := range s.items {
		if it.typ == "message" {
			out += renderResponsesEvent("response.output_text.done", map[string]any{
				"type":          "response.output_text.done",
				"item_id":       it.id,
				"output_index":  it.index,
				"content_index": 0,
				"text":          it.text.String(),
			})
			out += renderResponsesEvent("response.content_part.done", map[string]any{
				"type":          "response.content_part.done",
				"item_id":       it.id,
				"output_index":  it.index,
				"content_index": 0,
				"part":          it.textPart(),
			})
		} else {
			out += renderResponsesEvent("response.function_call_arguments.done", map[string]any{
				"type":         "response.function_call_arguments.done",
				"item_id":      it.id,
				"output_index": it.index,
				"arguments":    it.args.String(),
			})
		}
		out += renderResponsesEvent("response.output_item.done", map[string]any{
			"type":         "response.output_item.done",
			"output_index": it.index,
			"item":         it.completedItem(),
		})
	}

	out += renderResponsesEvent("response.completed", map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"id":         s.respID,
			"object":     "response",
			"created_at": s.createdAt,
			"status":     status,
			"model":      s.model,
			"output":     output,
			"usage": map[string]any{
				"input_tokens":  s.prompt,
				"output_tokens": s.completion,
				"total_tokens":  s.prompt + s.completion,
			},
		},
	})
	out += "data: [DONE]\n\n"
	return out
}

// completedItem renders the completed shape of an output item.
func (it *r2oItem) completedItem() map[string]any {
	if it.typ == "function_call" {
		return map[string]any{
			"type":      "function_call",
			"id":        it.id,
			"call_id":   it.callID,
			"name":      it.name,
			"arguments": it.args.String(),
			"status":    "completed",
		}
	}
	return map[string]any{
		"type":    "message",
		"id":      it.id,
		"status":  "completed",
		"role":    "assistant",
		"content": []any{it.textPart()},
	}
}

// unmarshalSSE is a thin alias so the chat chunk parse stays local to this
// file's translator.
func unmarshalSSE(data string, v any) error {
	return jsonUnmarshal(data, v)
}
