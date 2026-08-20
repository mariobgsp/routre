package proxy

import (
	"encoding/json"
	"strings"
	"testing"

	"routre-cli/internal/mock"
)

// newResponsesEnv builds a gateway against a single openai-kind mock.
func newResponsesEnv(t *testing.T) (base string, m *mock.Server) {
	t.Helper()
	a, err := mock.New("resp-a")
	if err != nil {
		t.Fatalf("mock: %v", err)
	}
	t.Cleanup(a.Close)
	cfg := `{
	  "listen":"127.0.0.1:0",
	  "tiers":[{"name":"t","providers":[{"name":"a","kind":"openai","base_url":"` + a.URL() + `/v1","api_key_env":"TEST_KEY_A","models":["muse-spark-1.2-contributor"]}]}],
	  "rtk":{"enabled":true,"min_bytes":500,"max_bytes":10485760},
	  "cache":{"enabled":true,"max_entries":64,"ttl_seconds":3600,"prefix_order":false}
	}`
	t.Setenv("TEST_KEY_A", "k")
	base, _ = testEnv(t, cfg)
	return base, a
}

func TestResponsesNonStreamingRequestTranslation(t *testing.T) {
	base, m := newResponsesEnv(t)

	body := `{"model":"muse-spark-1.2-contributor","instructions":"Be terse","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}],"max_output_tokens":200}`

	resp, data := post(t, base, "/v1/responses", []byte(body))
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, data)
	}

	// The upstream must have received a decoded chat.completions request.
	lb := m.LastBody.Load().([]byte)
	var chat struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
		Messages  []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(lb, &chat); err != nil {
		t.Fatalf("upstream body not chat JSON: %v (%s)", err, lb)
	}
	if chat.Model != "muse-spark-1.2-contributor" {
		t.Fatalf("model not carried: %v", chat.Model)
	}
	if len(chat.Messages) != 2 || chat.Messages[0].Role != "system" || chat.Messages[1].Role != "user" {
		t.Fatalf("expected [system,user] messages, got %+v", chat.Messages)
	}

	// The client must receive a Responses envelope.
	var env struct {
		Object      string `json:"object"`
		Status      string `json:"status"`
		Model       string `json:"model"`
		Output      []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("response not Responses envelope: %v (%s)", err, data)
	}
	if env.Object != "response" || env.Status != "completed" {
		t.Fatalf("bad envelope: object=%q status=%q", env.Object, env.Status)
	}
	if len(env.Output) != 1 || env.Output[0].Type != "message" {
		t.Fatalf("expected 1 message output item, got %+v", env.Output)
	}
	if got := env.Output[0].Content[0].Text; !strings.Contains(got, "mock response") {
		t.Fatalf("expected mock text in output, got %q", got)
	}
}

func TestResponsesStringInput(t *testing.T) {
	base, _ := newResponsesEnv(t)

	body := `{"model":"muse-spark-1.2-contributor","input":"plain string question"}`
	_, data := post(t, base, "/v1/responses", []byte(body))
	if !strings.Contains(string(data), "response") {
		t.Fatalf("expected responses envelope, got: %s", data)
	}
}

func TestResponsesStreamingSSE(t *testing.T) {
	base, m := newResponsesEnv(t)
	m.SetStream(true)

	body := `{"model":"muse-spark-1.2-contributor","input":"hello","stream":true}`
	resp, data := post(t, base, "/v1/responses", []byte(body))
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, data)
	}

	s := string(data)
	for _, want := range []string{
		"response.created",
		"response.in_progress",
		"response.output_text.delta",
		"response.completed",
		"[DONE]",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("stream missing %q:\n%s", want, s)
		}
	}

	// The completed envelope must carry assembled text + usage.
	if !strings.Contains(s, `"output_tokens":8`) {
		t.Fatalf("expected usage carried in stream:\n%s", s)
	}
	if !strings.Contains(s, `"status":"completed"`) {
		t.Fatalf("expected completed status:\n%s", s)
	}
}

func TestResponsesAnthropicUpstreamRejected(t *testing.T) {
	m, err := mock.New("resp-anthropic")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	cfg := `{
	  "listen":"127.0.0.1:0",
	  "tiers":[{"name":"t","providers":[{"name":"a","kind":"anthropic","base_url":"` + m.URL() + `","api_key_env":"TEST_KEY_A","models":["m"]}]}],
	  "rtk":{"enabled":true,"min_bytes":500,"max_bytes":10485760},
	  "cache":{"enabled":true,"max_entries":64,"ttl_seconds":3600,"prefix_order":false}
	}`
	t.Setenv("TEST_KEY_A", "k")
	base, _ := testEnv(t, cfg)

	body := `{"model":"m","input":"hi"}`
	resp, data := post(t, base, "/v1/responses", []byte(body))
	if resp.StatusCode == 200 {
		t.Fatalf("anthropic upstream must not answer a Responses request, got 200: %s", data)
	}
}
