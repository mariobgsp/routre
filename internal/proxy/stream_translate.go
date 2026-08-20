package proxy

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Streaming cross-kind translation.
//
// Same-kind streaming is a byte-for-byte copy (streamRelay). Cross-kind
// (OpenAI <-> Anthropic) streaming used to be rejected with 501; instead we
// parse the upstream SSE frame-by-frame and re-emit the events in the
// client's dialect, never buffering the whole response (~small state + a
// per-tool argument buffer). This mirrors the approach used by kiroxy,
// promsoft/free-llm-proxy and CLASP.
//
// The tool-call ID is passed through unchanged (never re-minted): the client
// echoes it back as tool_call_id / tool_use_id, so the loop round-trips.
// Partial JSON fragments (OpenAI tool_calls[].arguments / Anthropic
// input_json_delta) are forwarded verbatim; only the client accumulates them
// into objects.
//
// Failover contract (unchanged): the caller must NOT emit any byte to the
// client before the first parseable frame; a translate error before that
// first byte is retryable (fail over), after it is a stream abort. See
// streamRelay.

// streamTranslator holds per-stream state for one direction of translation.
type streamTranslator struct {
	// from is the client dialect, to the upstream dialect. The upstream
	// produces `to` frames; we emit `from` frames to the client.
	from, to apiFormat
	// emitted records whether any byte reached the client yet.
	emitted bool
	// a2o holds Anthropic->OpenAI state when to==anthropic.
	a2o a2oState
	// o2a holds OpenAI->Anthropic state when to==openai.
	o2a o2aState
	// r2o holds Responses->OpenAI state when to==openai.
	r2o r2oState
	// g2o holds Gemini->OpenAI state when to==gemini (OpenAI client, Gemini
	// upstream).
	g2o g2oState
}

func newStreamTranslator(from, to apiFormat) *streamTranslator {
	st := &streamTranslator{from: from, to: to}
	// o2a tracks the open content-block index and current tool index with a
	// -1 sentinel for "none"; a zero-value struct would read 0 and suppress
	// block/tool starts.
	st.o2a.openBlockIdx = -1
	st.o2a.curToolIdx = -1
	return st
}

// translate processes one parsed upstream frame, returning the SSE bytes to
// write to the client (possibly empty). A non-nil err is a translation failure
// (never an upstream-read failure, which translateStream handles).
func (st *streamTranslator) translate(evt sseEvent) (string, error) {
	switch {
	case st.from == fmtOpenAI && st.to == fmtAnthropic:
		return st.a2o.translate(evt)
	case st.from == fmtAnthropic && st.to == fmtOpenAI:
		return st.o2a.translate(evt)
	case st.from == fmtResponses && st.to == fmtOpenAI:
		return st.r2o.translate(evt)
	case st.from == fmtOpenAI && st.to == fmtGemini:
		return st.g2o.translate(evt)
	default:
		return "", fmt.Errorf("unsupported stream translation %v -> %v", st.from, st.to)
	}
}

// translateStream translates an upstream SSE stream into the client's dialect,
// writing to w. flush, if non-nil, is called after every frame to push SSE
// chunks to the client in real time (streaming must not buffer until EOF). It
// returns nil on success or a retryable pre-first-byte failure.
func translateStream(w io.Writer, upstream io.Reader, from, to apiFormat, flush func()) error {
	// Precondition: from != to. streamRelay routes same-kind to the byte-copy
	// loop; cross-kind (only) reaches translateStream.
	st := newStreamTranslator(from, to)
	br := bufio.NewReader(upstream)
	for {
		evt := sseEvent{}
		ok, ferr := evt.read(br)
		if !ok && ferr == nil {
			continue // blank frame
		}
		if ferr != nil {
			if ferr == io.EOF {
				return nil // clean end of stream
			}
			// Upstream read failure after first byte: not retryable.
			if st.emitted {
				// Client already received data; cannot fail over.
				return nil
			}
			return ferr
		}
		out, perr := st.translate(evt)
		if perr != nil {
			if !st.emitted {
				return perr // retryable: fail over to next candidate
			}
			return nil // mid-stream: can't fail over
		}
		if len(out) > 0 {
			if _, werr := io.WriteString(w, out); werr != nil {
				// Client went away: not an upstream failure.
				return nil
			}
			st.emitted = true
			if flush != nil {
				flush()
			}
		}
	}
}

// sseEvent is one SSE frame: an optional event name plus a data payload.
type sseEvent struct {
	event string
	data  []string // raw data lines (without the "data:" prefix)
}

// read parses one SSE frame (terminated by a blank line or EOF) from br.
// ok=false means the frame was empty (e.g. an initial blank line) and should
// be skipped. err is non-nil io.EOF at a clean stream end (with ok=true if a
// final frame without trailing blank line was parsed).
func (e *sseEvent) read(br *bufio.Reader) (bool, error) {
	e.event = ""
	e.data = nil
	saw := false
	for {
		line, err := br.ReadString('\n')
		if err != nil && err != io.EOF {
			return saw, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if saw {
				return true, nil
			}
			if err != nil {
				return false, err
			}
			continue
		}
		saw = true
		switch {
		case strings.HasPrefix(line, "event:"):
			e.event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			e.data = append(e.data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		default:
			// SSE comment or unknown field; ignored.
		}
		if err == io.EOF {
			return saw, io.EOF
		}
	}
}

// dataJSON concatenates the frame's data lines (for a JSON payload this is a
// single line).
func (e *sseEvent) dataJSON() string { return strings.Join(e.data, "\n") }

// sseFrame renders an SSE frame with an optional event name.
func sseFrame(event, data string) string {
	var b strings.Builder
	if event != "" {
		b.WriteString("event: ")
		b.WriteString(event)
		b.WriteString("\n")
	}
	b.WriteString("data: ")
	b.WriteString(data)
	b.WriteString("\n\n")
	return b.String()
}

func mustMarshal(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func strPtr(s string) *string { return &s }

// ---------------------------------------------------------------------------
// Anthropic -> OpenAI (client speaks OpenAI, upstream is Anthropic /v1/messages)
// ---------------------------------------------------------------------------

type openAIChunk struct {
	ID      string              `json:"id"`
	Object  string              `json:"object"`
	Model   string              `json:"model,omitempty"`
	Choices []openAIChunkChoice `json:"choices,omitempty"`
	Usage   *openAIUsage        `json:"usage,omitempty"`
}

type openAIChunkChoice struct {
	Index        int         `json:"index"`
	Delta        openAIDelta `json:"delta"`
	FinishReason *string     `json:"finish_reason"`
}

type openAIDelta struct {
	Role      string           `json:"role,omitempty"`
	Content   *string          `json:"content,omitempty"`
	ToolCalls []openAIToolCall `json:"tool_calls,omitempty"`
}

type openAIToolCall struct {
	Index    int                `json:"index"`
	ID       string             `json:"id,omitempty"`
	Type     string             `json:"type,omitempty"`
	Function openAIToolFunction `json:"function"`
}

type openAIToolFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type openAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// a2oState is Anthropic->OpenAI per-stream state.
type a2oState struct {
	chatID       string
	model        string
	emittedRole  bool
	nextToolIdx  int // OpenAI tool index counter (text blocks don't consume it)
	curToolIdx   int // index of the currently-open Anthropic tool block in OpenAI terms
	finishReason string
	outputTokens int
	promptTokens int
	sawToolUse   bool
	done         bool // message_stop / [DONE]-equivalent already emitted
	emittedDONE  bool
}

func (s *a2oState) translate(evt sseEvent) (string, error) {
	data := evt.dataJSON()
	if data == "" {
		return "", nil
	}
	var m struct {
		Type    string `json:"type"`
		Message struct {
			ID    string `json:"id"`
			Model string `json:"model"`
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		} `json:"message"`
		Index        int `json:"index"`
		ContentBlock struct {
			Type string `json:"type"`
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"content_block"`
		Delta struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			PartialJSON string `json:"partial_json"`
			StopReason  string `json:"stop_reason"`
		} `json:"delta"`
		Usage struct {
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		return "", fmt.Errorf("parse anthropic event %q: %w", data, err)
	}

	var out string
	switch evt.event {
	case "message_start":
		s.chatID = m.Message.ID
		s.model = m.Message.Model
		s.promptTokens = m.Message.Usage.InputTokens
		if !s.emittedRole {
			s.emittedRole = true
			chunk := openAIChunk{ID: s.chatID, Object: "chat.completion.chunk", Model: s.model,
				Choices: []openAIChunkChoice{{Index: 0, Delta: openAIDelta{Role: "assistant"}}}}
			out += sseFrame("", mustMarshal(chunk))
		}
	case "content_block_start":
		if m.ContentBlock.Type == "tool_use" {
			s.sawToolUse = true
			s.curToolIdx = s.nextToolIdx
			chunk := openAIChunk{ID: s.chatID, Object: "chat.completion.chunk",
				Choices: []openAIChunkChoice{{Index: 0, Delta: openAIDelta{ToolCalls: []openAIToolCall{
					{Index: s.nextToolIdx, ID: m.ContentBlock.ID, Type: "function", Function: openAIToolFunction{Name: m.ContentBlock.Name}},
				}}}}}
			out += sseFrame("", mustMarshal(chunk))
			s.nextToolIdx++
		}
	case "content_block_delta":
		switch m.Delta.Type {
		case "text_delta":
			if m.Delta.Text != "" {
				chunk := openAIChunk{ID: s.chatID, Object: "chat.completion.chunk",
					Choices: []openAIChunkChoice{{Index: 0, Delta: openAIDelta{Content: &m.Delta.Text}}}}
				out += sseFrame("", mustMarshal(chunk))
			}
		case "input_json_delta":
			if m.Delta.PartialJSON != "" {
				chunk := openAIChunk{ID: s.chatID, Object: "chat.completion.chunk",
					Choices: []openAIChunkChoice{{Index: 0, Delta: openAIDelta{ToolCalls: []openAIToolCall{
						{Index: s.curToolIdx, Function: openAIToolFunction{Arguments: m.Delta.PartialJSON}},
					}}}}}
				out += sseFrame("", mustMarshal(chunk))
			}
		}
	case "message_delta":
		if m.Delta.StopReason != "" {
			switch m.Delta.StopReason {
			case "tool_use":
				s.finishReason = "tool_calls"
			case "max_tokens":
				s.finishReason = "length"
			case "end_turn", "stop_sequence":
				s.finishReason = "stop"
			}
		}
		s.outputTokens = m.Usage.OutputTokens
	case "message_stop":
		fr := s.finishReason
		if fr == "" {
			if s.sawToolUse {
				fr = "tool_calls"
			} else {
				fr = "stop"
			}
		}
		final := openAIChunk{ID: s.chatID, Object: "chat.completion.chunk",
			Choices: []openAIChunkChoice{{Index: 0, Delta: openAIDelta{}, FinishReason: &fr}}}
		out += sseFrame("", mustMarshal(final))
		usage := openAIChunk{ID: s.chatID, Object: "chat.completion.chunk",
			Usage: &openAIUsage{PromptTokens: s.promptTokens, CompletionTokens: s.outputTokens, TotalTokens: s.promptTokens + s.outputTokens}}
		out += sseFrame("", mustMarshal(usage))
		out += "data: [DONE]\n\n"
		s.emittedDONE = true
		s.done = true
	case "error":
		fr := "content_filter"
		final := openAIChunk{ID: s.chatID, Object: "chat.completion.chunk",
			Choices: []openAIChunkChoice{{Index: 0, Delta: openAIDelta{}, FinishReason: &fr}}}
		out += sseFrame("", mustMarshal(final))
		out += "data: [DONE]\n\n"
		s.emittedDONE = true
		s.done = true
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// OpenAI -> Anthropic (client speaks Anthropic, upstream is OpenAI chat.completions)
// ---------------------------------------------------------------------------

type anthropicEvent struct {
	Type    string `json:"type"`
	Message *struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message,omitempty"`
	Index        int `json:"index"`
	ContentBlock *struct {
		Type string `json:"type"`
		Text string `json:"text"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"content_block,omitempty"`
	Delta *struct {
		Type        *string `json:"type"`
		Text        *string `json:"text"`
		PartialJSON *string `json:"partial_json"`
		StopReason  *string `json:"stop_reason,omitempty"`
	} `json:"delta,omitempty"`
	Usage struct {
		OutputTokens int `json:"output_tokens,omitempty"`
	} `json:"usage,omitempty"`
}

// o2aState is OpenAI->Anthropic per-stream state.
type o2aState struct {
	chatID       string // fabricated msg_ id
	started      bool
	blockCounter int // monotonically increasing Anthropic content-block index
	openBlockIdx int // index of currently-open block, -1 if none
	openIsTool   bool
	curToolIdx   int // OpenAI tool index currently being streamed (-1 if none)
	finishReason string
	outputTokens int
	done         bool
	emittedStop  bool
}

func (s *o2aState) translate(evt sseEvent) (string, error) {
	data := evt.dataJSON()
	if data == "" || data == "[DONE]" {
		if data == "[DONE]" {
			return s.finish(), nil
		}
		return "", nil
	}
	var m struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
		Choices []struct {
			Index int `json:"index"`
			Delta struct {
				Role      string  `json:"role"`
				Content   *string `json:"content"`
				ToolCalls []struct {
					Index    int    `json:"index"`
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		return "", fmt.Errorf("parse openai chunk %q: %w", data, err)
	}

	var out string
	emit := func(e anthropicEvent) { out += renderAnthropic(e) }

	if !s.started {
		s.started = true
		s.chatID = "msg_stream"
		emit(anthropicEvent{Type: "message_start", Message: &struct {
			ID    string `json:"id"`
			Model string `json:"model"`
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}{ID: s.chatID, Model: m.Model}})
	}

	if m.Usage != nil {
		s.outputTokens = m.Usage.CompletionTokens
	}

	if len(m.Choices) > 0 {
		c := m.Choices[0]
		d := c.Delta

		// Tool calls: open a tool_use block on the first fragment carrying an
		// id/name; stream subsequent fragments of the same index as
		// input_json_delta.
		for _, tc := range d.ToolCalls {
			idx := tc.Index
			if idx != s.curToolIdx {
				// New tool-call index: close any open block, open a tool_use.
				if s.openBlockIdx >= 0 {
					emit(anthropicEvent{Type: "content_block_stop", Index: s.openBlockIdx})
				}
				s.curToolIdx = idx
				id := tc.ID
				if id == "" {
					id = "toolu_stream_" + strconv.Itoa(idx)
				}
				blockIdx := s.blockCounter
				s.blockCounter++
				s.openBlockIdx = blockIdx
				s.openIsTool = true
				emit(anthropicEvent{Type: "content_block_start", Index: blockIdx, ContentBlock: &struct {
					Type string `json:"type"`
					Text string `json:"text"`
					ID   string `json:"id"`
					Name string `json:"name"`
				}{Type: "tool_use", ID: id, Name: tc.Function.Name}})
			}
			if tc.Function.Arguments != "" {
				partial := tc.Function.Arguments
				emit(anthropicEvent{Type: "content_block_delta", Index: s.openBlockIdx, Delta: &struct {
					Type        *string `json:"type"`
					Text        *string `json:"text"`
					PartialJSON *string `json:"partial_json"`
					StopReason  *string `json:"stop_reason,omitempty"`
				}{Type: strPtr("input_json_delta"), PartialJSON: &partial}})
			}
		}

		if d.Content != nil && *d.Content != "" {
			if s.openIsTool {
				// Text after a tool block: close the tool block and open a
				// fresh text block (Anthropic content blocks never mix kinds).
				emit(anthropicEvent{Type: "content_block_stop", Index: s.openBlockIdx})
				s.openIsTool = false
				s.openBlockIdx = -1
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
				}{Type: "text", Text: ""}})
			}
			text := *d.Content
			emit(anthropicEvent{Type: "content_block_delta", Index: s.openBlockIdx, Delta: &struct {
				Type        *string `json:"type"`
				Text        *string `json:"text"`
				PartialJSON *string `json:"partial_json"`
				StopReason  *string `json:"stop_reason,omitempty"`
			}{Type: strPtr("text_delta"), Text: &text}})
		}

		if c.FinishReason != nil && *c.FinishReason != "" {
			switch *c.FinishReason {
			case "stop":
				s.finishReason = "end_turn"
			case "length":
				s.finishReason = "max_tokens"
			case "tool_calls":
				s.finishReason = "tool_use"
			case "content_filter":
				s.finishReason = "end_turn"
			}
		}
	}
	return out, nil
}

// finish emits content_block_stop for the open block, message_delta with the
// stop reason, and message_stop. Idempotent.
func (s *o2aState) finish() string {
	if s.done {
		return ""
	}
	s.done = true
	var out string
	if s.openBlockIdx >= 0 {
		out += renderAnthropic(anthropicEvent{Type: "content_block_stop", Index: s.openBlockIdx})
		s.openBlockIdx = -1
	}
	fr := s.finishReason
	if fr == "" {
		fr = "end_turn"
	}
	out += renderAnthropic(anthropicEvent{Type: "message_delta", Delta: &struct {
		Type        *string `json:"type"`
		Text        *string `json:"text"`
		PartialJSON *string `json:"partial_json"`
		StopReason  *string `json:"stop_reason,omitempty"`
	}{StopReason: &fr}})
	out += renderAnthropic(anthropicEvent{Type: "message_stop"})
	return out
}

func renderAnthropic(e anthropicEvent) string { return sseFrame(e.Type, mustMarshal(e)) }

// jsonUnmarshal decodes a JSON string/bytes payload into v.
func jsonUnmarshal(data string, v any) error {
	err := json.Unmarshal([]byte(data), v)
	return err
}
