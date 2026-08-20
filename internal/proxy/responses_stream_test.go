package proxy

import (
	"strings"
	"testing"
)

// TestR2OStreamingToolCalls drives the chat->responses streaming translator
// with a tool-call stream and asserts the emitted Responses event sequence
// (output_item.added for the function_call, arguments deltas, done events,
// a completed response with the assembled tool item).
func TestR2OStreamingToolCalls(t *testing.T) {
	upstream := strings.Join([]string{
		"data: " + `{"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}` + "\n\n",
		"data: " + `{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"bash","arguments":""}}]},"finish_reason":null}]}` + "\n\n",
		"data: " + `{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"c"}}]},"finish_reason":null}]}` + "\n\n",
		"data: " + `{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"md\":\"ls\"}"}}]},"finish_reason":null}]}` + "\n\n",
		"data: " + `{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":9,"completion_tokens":6,"total_tokens":15}}` + "\n\n",
		"data: [DONE]\n\n",
	}, "")

	got := translateForTest(t, upstream, fmtResponses, fmtOpenAI)

	for _, want := range []string{
		"response.created",
		"response.output_item.added",
		"response.function_call_arguments.delta",
		"response.function_call_arguments.done",
		"response.output_item.done",
		"response.completed",
		"[DONE]",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}

	// The completed response must carry the assembled function_call output.
	if !strings.Contains(got, `"type":"function_call"`) {
		t.Fatalf("function_call output item missing:\n%s", got)
	}
	if !strings.Contains(got, `"arguments":"{\"cmd\":\"ls\"}"`) {
		t.Fatalf("arguments not assembled:\n%s", got)
	}
	if !strings.Contains(got, `"status":"completed"`) {
		t.Fatalf("expected completed status:\n%s", got)
	}
	if !strings.Contains(got, `"input_tokens":9`) {
		t.Fatalf("usage not carried:\n%s", got)
	}
}

// TestR2OStreamingOnlyText covers the plain-text path (no tools): message
// item, output_text deltas, and a single completed message in output.
func TestR2OStreamingOnlyText(t *testing.T) {
	upstream := strings.Join([]string{
		"data: " + `{"id":"c1","choices":[{"index":0,"delta":{"role":"assistant","content":"Hel"},"finish_reason":null}]}` + "\n\n",
		"data: " + `{"id":"c1","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":null}]}` + "\n\n",
		"data: " + `{"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n",
		"data: [DONE]\n\n",
	}, "")

	got := translateForTest(t, upstream, fmtResponses, fmtOpenAI)

	for _, want := range []string{"response.output_text.delta", "response.output_text.done", "response.completed", "\"text\":\"Hello\""} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}
}
