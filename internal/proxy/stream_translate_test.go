package proxy

import (
	"bufio"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"routre-cli/internal/mock"
)

// buildAnthropicConfigWithMocks is buildConfigWithMocks but with anthropic
// kind upstreams.
func buildAnthropicConfigWithMocks(t *testing.T, mocks map[string]*mock.Server) string {
	t.Helper()
	var tiers []string
	order := []string{"a", "b", "c"}
	for _, name := range order {
		m, ok := mocks[name]
		if !ok {
			continue
		}
		tiers = append(tiers, `{"name":"tier-`+name+`","providers":[{"name":"`+name+`","kind":"anthropic","base_url":"`+m.URL()+`/v1","api_key_env":"TEST_KEY_`+strings.ToUpper(name)+`","models":["m"]}]}`)
	}
	return `{"listen":"127.0.0.1:0","tiers":[` + strings.Join(tiers, ",") + `],"rtk":{"enabled":true,"min_bytes":500,"max_bytes":10485760},"cache":{"enabled":true,"max_entries":64,"ttl_seconds":3600,"prefix_order":false}}`
}

// translateForTest runs an SSE stream through translateStream and returns all
// emitted bytes.
func translateForTest(t *testing.T, upstream string, from, to apiFormat) string {
	t.Helper()
	st := newStreamTranslator(from, to)
	var out strings.Builder
	rd := bufio.NewReader(strings.NewReader(upstream))
	for {
		evt := sseEvent{}
		ok, ferr := evt.read(rd)
		if !ok && ferr == nil {
			continue
		}
		if ferr != nil {
			break
		}
		s, perr := st.translate(evt)
		if perr != nil {
			t.Fatalf("translate: %v", perr)
		}
		out.WriteString(s)
	}
	return out.String()
}

func TestStreamTranslateAnthropicToOpenAI(t *testing.T) {
	upstream := strings.Join([]string{
		"event: message_start\ndata: " + `{"type":"message_start","message":{"id":"msg_abc","type":"message","role":"assistant","model":"claude-3","content":[],"usage":{"input_tokens":10,"output_tokens":0}}}` + "\n\n",
		"event: content_block_start\ndata: " + `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n",
		"event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello "}}` + "\n\n",
		"event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"world"}}` + "\n\n",
		"event: content_block_start\ndata: " + `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_x1","name":"bash","input":{}}}` + "\n\n",
		"event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"command\":"}}` + "\n\n",
		"event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"ls\"}"}}` + "\n\n",
		"event: content_block_stop\ndata: " + `{"type":"content_block_stop","index":1}` + "\n\n",
		"event: message_delta\ndata: " + `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":5}}` + "\n\n",
		"event: message_stop\ndata: " + `{"type":"message_stop"}` + "\n\n",
	}, "")

	got := translateForTest(t, upstream, fmtOpenAI, fmtAnthropic)

	// Parse the emitted chunks, expect the tool id passthrough + finish_reason
	// tool_calls + [DONE].
	if !strings.Contains(got, `"id":"toolu_x1"`) {
		t.Fatalf("tool id not passed through unchanged:\n%s", got)
	}
	if !strings.Contains(got, `"name":"bash"`) {
		t.Fatalf("tool name missing:\n%s", got)
	}
	if !strings.Contains(got, `"tool_calls"`) {
		t.Fatalf("no tool_calls emitted:\n%s", got)
	}
	if !strings.Contains(got, `"finish_reason":"tool_calls"`) {
		t.Fatalf("finish_reason not mapped to tool_calls:\n%s", got)
	}
	if !strings.HasSuffix(got, "data: [DONE]\n\n") {
		t.Fatalf("stream not terminated with [DONE]:\n%s", got)
	}
	if strings.Contains(got, "input_json_delta") || strings.Contains(got, "content_block") {
		t.Fatalf("anthropic frames leaked to client:\n%s", got)
	}
	// text fragments preserved
	if !strings.Contains(got, `"content":"Hello "`) || !strings.Contains(got, `"content":"world"`) {
		t.Fatalf("text deltas not preserved:\n%s", got)
	}
	// partial JSON arguments passed verbatim, same tool index (0)
	if !strings.Contains(got, `"arguments":"{\"command\":"`) || !strings.Contains(got, `"arguments":"\"ls\"}"`) {
		t.Fatalf("partial args not passed verbatim:\n%s", got)
	}
}

func TestStreamTranslateOpenAIToAnthropic(t *testing.T) {
	upstream := strings.Join([]string{
		"data: " + `{"id":"chatcmpl-x","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"},"finish_reason":null}]}` + "\n\n",
		"data: " + `{"id":"chatcmpl-x","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":null}]}` + "\n\n",
		"data: " + `{"id":"chatcmpl-x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_9","type":"function","function":{"name":"bash","arguments":"{\"cmd\":\"ls\""}}]},"finish_reason":null}]}` + "\n\n",
		"data: " + `{"id":"chatcmpl-x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"}"}}]},"finish_reason":null}]}` + "\n\n",
		"data: " + `{"id":"chatcmpl-x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n",
		"data: [DONE]\n\n",
	}, "")

	got := translateForTest(t, upstream, fmtAnthropic, fmtOpenAI)

	// message_start
	if !strings.Contains(got, "event: message_start") {
		t.Fatalf("no message_start:\n%s", got)
	}
	// tool id passthrough
	if !strings.Contains(got, `"id":"call_9"`) {
		t.Fatalf("tool call id not preserved:\n%s", got)
	}
	if !strings.Contains(got, `"type":"tool_use"`) {
		t.Fatalf("no tool_use block:\n%s", got)
	}
	// partial args -> input_json_delta preserved verbatim
	if !strings.Contains(got, `"type":"input_json_delta"`) || !strings.Contains(got, `"partial_json":"{\"cmd\":\"ls\""`) {
		t.Fatalf("partial args not mapped to input_json_delta:\n%s", got)
	}
	// text delta
	if !strings.Contains(got, `"type":"text_delta"`) {
		t.Fatalf("no text_delta:\n%s", got)
	}
	// finish reason tool_calls -> tool_use, message_stop
	if !strings.Contains(got, `"stop_reason":"tool_use"`) {
		t.Fatalf("finish_reason not mapped to tool_use stop_reason:\n%s", got)
	}
	if !strings.Contains(got, "event: message_stop") {
		t.Fatalf("no message_stop:\n%s", got)
	}
	// Ensure the last frame is content_block_stop -> ... -> message_stop
	if !strings.Contains(got, "event: message_delta") {
		t.Fatalf("no message_delta:\n%s", got)
	}

	// Validate it parses as well-formed anthropic SSE events.
	rd := bufio.NewReader(strings.NewReader(got))
	n := 0
	for {
		ev := sseEvent{}
		ok, ferr := ev.read(rd)
		if !ok && ferr == nil {
			continue
		}
		if ferr != nil {
			break
		}
		var pm map[string]any
		if err := json.Unmarshal([]byte(ev.dataJSON()), &pm); err != nil {
			t.Fatalf("emitted %s event not valid JSON: %v\n%s", ev.event, err, ev.dataJSON())
		}
		n++
	}
	if n == 0 {
		t.Fatalf("no anthropic events emitted")
	}
}

// e2e: OpenAI-dialect client -> gateway -> anthropic mock, streaming. Asserts
// the client receives translated OpenAI chunks (not the 501).
func TestStreamCrossKindE2E(t *testing.T) {
	a, _ := mock.New("a")
	defer a.Close()
	a.SetStream(true)
	a.SetAnthropic(true)
	base, _ := testEnv(t, buildAnthropicConfigWithMocks(t, map[string]*mock.Server{"a": a}))

	resp, data := post(t, base, "/v1/chat/completions", chatBody(true, ""))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200:\n%s", resp.StatusCode, data)
	}
	s := string(data)
	if strings.Contains(s, "501") || strings.Contains(s, "not_implemented") {
		t.Fatalf("cross-kind streaming still 501:\n%s", s)
	}
	// Should be OpenAI dialect chunks with translated text + tool_calls + DONE.
	if !strings.Contains(s, `"object":"chat.completion.chunk"`) {
		t.Fatalf("no openai chunks emitted:\n%s", s)
	}
	if !strings.Contains(s, `"id":"toolu_mock_1"`) {
		t.Fatalf("tool id from anthropic mock not passed through:\n%s", s)
	}
	if !strings.Contains(s, `"name":"bash"`) {
		t.Fatalf("tool name missing:\n%s", s)
	}
	if !strings.Contains(s, "data: [DONE]") {
		t.Fatalf("no [DONE] terminator:\n%s", s)
	}
	if strings.Contains(s, "content_block") || strings.Contains(s, "message_start") {
		t.Fatalf("anthropic frames leaked to openai client:\n%s", s)
	}
}

// e2e: Anthropic-dialect client -> gateway -> openai mock, streaming.
func TestStreamCrossKindE2EAnthropicClient(t *testing.T) {
	a, _ := mock.New("a")
	defer a.Close()
	a.SetStream(true)
	// openai-format mock upstream, default.
	base, _ := testEnv(t, buildConfigWithMocks(t, map[string]*mock.Server{"a": a}))

	body := []byte(`{"model":"m","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	resp, data := post(t, base, "/v1/messages", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200:\n%s", resp.StatusCode, data)
	}
	s := string(data)
	if !strings.Contains(s, "event: message_start") {
		t.Fatalf("no message_start for anthropic client:\n%s", s)
	}
	if !strings.Contains(s, "content_block_delta") {
		t.Fatalf("no content_block_delta:\n%s", s)
	}
	if !strings.Contains(s, "event: message_stop") {
		t.Fatalf("no message_stop:\n%s", s)
	}
}
