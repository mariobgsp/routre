package dialect

import (
	"strings"
	"testing"
)

func TestDetectFormat(t *testing.T) {
	if got := DetectFormat("/v1/messages", []byte(`{}`)); got != FormatAnthropic {
		t.Fatalf("DetectFormat /v1/messages = %v want Anthropic", got)
	}
	if got := DetectFormat("/v1/responses", []byte(`{}`)); got != FormatResponses {
		t.Fatalf("got %v want Responses", got)
	}
	if got := DetectFormat("/v1/chat/completions", []byte(`{}`)); got != FormatOpenAI {
		t.Fatalf("got %v want OpenAI", got)
	}
}

func TestIsStreaming(t *testing.T) {
	if !IsStreaming([]byte(`{"model":"m","stream":true}`)) {
		t.Fatal("want streaming true")
	}
	if IsStreaming([]byte(`{"model":"m","stream":false}`)) {
		t.Fatal("want streaming false")
	}
	if IsStreaming([]byte(`{"messages":[{"content":"\"stream\":true"}]}`)) {
		t.Fatal("content literal must not trigger streaming")
	}
}

func TestRequestSupportedPairs(t *testing.T) {
	d := New()
	for _, p := range d.Supported() {
		body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
		if p.From == FormatResponses {
			body = []byte(`{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
		}
		out, err := d.Request(p.From, p.To, body)
		if err != nil {
			t.Fatalf("Request %s->%s failed: %v", p.From, p.To, err)
		}
		if len(out) == 0 {
			t.Fatalf("Request %s->%s returned empty", p.From, p.To)
		}
	}
}

func TestRequestUnsupported(t *testing.T) {
	d := New()
	_, err := d.Request(FormatOpenAI, FormatResponses, []byte(`{}`))
	if err == nil {
		t.Fatal("want ErrUnsupported for OpenAI->Responses")
	}
	if !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("want unsupported error, got %v", err)
	}
}

func TestStreamUnsupported(t *testing.T) {
	d := New()
	err := d.Stream(FormatOpenAI, FormatResponses, strings.NewReader("data: {}\n\n"), &strings.Builder{}, nil)
	if err == nil {
		t.Fatal("want ErrUnsupported for Stream OpenAI->Responses")
	}
}

func TestKindToFormat(t *testing.T) {
	if KindToFormat("anthropic") != FormatAnthropic {
		t.Fatal("KindToFormat anthropic")
	}
	if KindToFormat("gemini") != FormatGemini {
		t.Fatal("KindToFormat gemini")
	}
	if KindToFormat("openai") != FormatOpenAI {
		t.Fatal("KindToFormat openai default")
	}
}
