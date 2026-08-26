package dialect

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAnthropicToGeminiRequest(t *testing.T) {
	body := []byte(`{
		"model":"gemini-2.5-flash","max_tokens":512,"temperature":0.5,
		"system":"be brief",
		"messages":[
			{"role":"user","content":"hi"},
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"city":"Oslo"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"sunny"}]}
		],
		"tools":[{"name":"get_weather","description":"weather lookup","input_schema":{"type":"object"}}]
	}`)
	out, err := New().Request(FormatAnthropic, FormatGemini, body)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	var doc struct {
		Model             string `json:"model"`
		SystemInstruction *struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"systemInstruction"`
		GenerationConfig struct {
			MaxOutputTokens int     `json:"maxOutputTokens"`
			Temperature     float64 `json:"temperature"`
		} `json:"generationConfig"`
		Contents []struct {
			Role  string `json:"role"`
			Parts []struct {
				Text             string         `json:"text"`
				FunctionCall     map[string]any `json:"functionCall,omitempty"`
				FunctionResponse map[string]any `json:"functionResponse,omitempty"`
			} `json:"parts"`
		} `json:"contents"`
		Tools []struct {
			FunctionDeclarations []map[string]any `json:"functionDeclarations"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, out)
	}
	if doc.Model != "gemini-2.5-flash" || doc.GenerationConfig.MaxOutputTokens != 512 || doc.GenerationConfig.Temperature != 0.5 {
		t.Fatalf("model/genConfig wrong: %+v", doc.GenerationConfig)
	}
	if doc.SystemInstruction == nil || doc.SystemInstruction.Parts[0].Text != "be brief" {
		t.Fatal("systemInstruction not mapped")
	}
	if len(doc.Contents) != 3 {
		t.Fatalf("want 3 contents, got %d", len(doc.Contents))
	}
	if doc.Contents[1].Parts[0].FunctionCall["name"] != "get_weather" {
		t.Fatalf("functionCall not mapped: %+v", doc.Contents[1].Parts[0])
	}
	resp := doc.Contents[2].Parts[0].FunctionResponse
	if resp == nil || resp["name"] != "get_weather" || resp["response"].(map[string]any)["output"] != "sunny" {
		t.Fatalf("functionResponse not mapped with tool name: %+v", resp)
	}
	if len(doc.Tools) != 1 || doc.Tools[0].FunctionDeclarations[0]["name"] != "get_weather" {
		t.Fatal("tools not mapped")
	}
}

func TestGeminiToAnthropicResponse(t *testing.T) {
	body := []byte(`{
		"candidates":[{"content":{"role":"model","parts":[
			{"text":"hello "},{"functionCall":{"name":"lookup","args":{"q":"x"}}}
		]},"finishReason":"STOP"}],
		"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":7}
	}`)
	out, err := GeminiToAnthropic(body, "gemini-2.5-flash")
	if err != nil {
		t.Fatalf("GeminiToAnthropic: %v", err)
	}
	var doc struct {
		ID         string `json:"id"`
		Type       string `json:"type"`
		Role       string `json:"role"`
		Model      string `json:"model"`
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type  string         `json:"type"`
			Text  string         `json:"text"`
			ID    string         `json:"id"`
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		} `json:"content"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, out)
	}
	if doc.Type != "message" || doc.Role != "assistant" || doc.Model != "gemini-2.5-flash" {
		t.Fatalf("envelope wrong: %+v", doc)
	}
	if doc.StopReason != "tool_use" {
		t.Fatalf("stop_reason = %q want tool_use", doc.StopReason)
	}
	if len(doc.Content) != 2 || doc.Content[0].Text != "hello " ||
		doc.Content[1].Type != "tool_use" || doc.Content[1].Name != "lookup" ||
		doc.Content[1].Input["q"] != "x" || !strings.HasPrefix(doc.Content[1].ID, "toolu_") {
		t.Fatalf("content blocks wrong: %+v", doc.Content)
	}
	if doc.Usage.InputTokens != 10 || doc.Usage.OutputTokens != 7 {
		t.Fatalf("usage wrong: %+v", doc.Usage)
	}
}

// streamOut collects g2a SSE output for assertions.
type streamOut struct {
	Events []struct {
		Type         string `json:"type"`
		Index        int    `json:"index"`
		ContentBlock *struct {
			Type string `json:"type"`
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"content_block"`
		Delta *struct {
			Type        *string `json:"type"`
			Text        *string `json:"text"`
			PartialJSON *string `json:"partial_json"`
			StopReason  *string `json:"stop_reason"`
		} `json:"delta"`
		Message *struct {
			Model string `json:"model"`
		} `json:"message"`
		Usage struct {
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
}

func (so *streamOut) types() []string {
	ts := make([]string, len(so.Events))
	for i, e := range so.Events {
		ts[i] = e.Type
	}
	return ts
}

func runG2AStream(t *testing.T, upstream string) streamOut {
	t.Helper()
	var sb strings.Builder
	if err := New().Stream(FormatAnthropic, FormatGemini, strings.NewReader(upstream), &sb, nil); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var so streamOut
	for _, frame := range strings.Split(sb.String(), "\n\n") {
		for _, line := range strings.Split(frame, "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			e := streamOut_Event{}
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &e); err != nil {
				t.Fatalf("decode event: %v (%s)", err, sb.String())
			}
			so.Events = append(so.Events, e)
		}
	}
	return so
}

// alias keeps the anonymous-shaped decode simple.
type streamOut_Event = streamOut_EventDef

func TestGeminiToAnthropicStream(t *testing.T) {
	upstream := "data: {\"modelVersion\":\"gemini-2.5-flash\",\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"hi\"}]}}]}\n\n" +
		"data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"functionCall\":{\"name\":\"lookup\",\"args\":{\"q\":\"x\"}}}]}}]}\n\n" +
		"data: {\"candidates\":[{\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":3,\"candidatesTokenCount\":9}}\n\n"
	so := runG2AStream(t, upstream)
	wantPrefix := []string{"message_start", "content_block_start", "content_block_delta", "content_block_stop", "content_block_start", "content_block_delta"}
	got := so.types()
	if len(got) < len(wantPrefix)+2 { // + message_delta + message_stop
		t.Fatalf("event sequence too short: %v", got)
	}
	for i, w := range wantPrefix {
		if got[i] != w {
			t.Fatalf("event[%d] = %s want %s (all: %v)", i, got[i], w, got)
		}
	}
	// Text delta carried through.
	if so.Events[2].Delta.Text == nil || *so.Events[2].Delta.Text != "hi" {
		t.Fatalf("text delta wrong: %+v", so.Events[2].Delta)
	}
	// Tool block opened with deterministic id and full-args input_json_delta.
	tb := so.Events[4].ContentBlock
	if tb.Type != "tool_use" || tb.Name != "lookup" || !strings.HasPrefix(tb.ID, "toolu_lookup") {
		t.Fatalf("tool_use block wrong: %+v", tb)
	}
	pj := so.Events[5].Delta.PartialJSON
	if pj == nil || !strings.Contains(*pj, `"q"`) {
		t.Fatalf("input_json_delta wrong: %+v", so.Events[5].Delta)
	}
	// Termination: stop reason tool_use + output tokens + message_stop.
	md := so.Events[len(got)-2]
	if md.Type != "message_delta" || md.Delta.StopReason == nil || *md.Delta.StopReason != "tool_use" || md.Usage.OutputTokens != 9 {
		t.Fatalf("message_delta wrong: %+v", md)
	}
	if got[len(got)-1] != "message_stop" {
		t.Fatalf("last event = %s want message_stop", got[len(got)-1])
	}
	if so.Events[0].Message.Model != "gemini-2.5-flash" {
		t.Fatalf("message_start model = %q", so.Events[0].Message.Model)
	}
}

// TestGeminiToAnthropicStreamEOFNoFinish covers an abnormal Gemini close
// without a finishReason frame: the EOF tail must still terminate the
// Anthropic event sequence.
func TestGeminiToAnthropicStreamEOFNoFinish(t *testing.T) {
	upstream := "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"partial\"}]}}]}\n\n"
	so := runG2AStream(t, upstream)
	got := so.types()
	last := got[len(got)-1]
	if last != "message_stop" {
		t.Fatalf("EOF without finishReason: last event = %s, all: %v", last, got)
	}
}

type streamOut_EventDef = struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentBlock *struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"content_block"`
	Delta *struct {
		Type        *string `json:"type"`
		Text        *string `json:"text"`
		PartialJSON *string `json:"partial_json"`
		StopReason  *string `json:"stop_reason"`
	} `json:"delta"`
	Message *struct {
		Model string `json:"model"`
	} `json:"message"`
	Usage struct {
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}
