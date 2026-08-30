package proxy

import (
	"io"
	"testing"
)

// usageFromBody is the non-streaming extraction seam used by
// ExtractNonStreaming. These tests cover the three dialects' prompt-
// cache fields so cache_creation_input_tokens is never silently dropped.
// Note: usageFromBody's early-return guard checks the OpenAI-style
// `prompt_tokens` field. Anthropic non-streaming responses are
// normalized to that shape by dialect.AnthropicToOpenAIResponse before
// reaching this parser, so the test bodies all use `prompt_tokens`.
func TestUsageFromBodyPromptCache(t *testing.T) {
	cases := []struct {
		name         string
		body         string
		wantRead     int64
		wantCreation int64
	}{
		{
			"openai_cached_and_creation",
			`{"usage":{"prompt_tokens":120,"completion_tokens":10,"prompt_tokens_details":{"cached_tokens":95,"cache_creation_input_tokens":40}}}`,
			95, 40,
		},
		{
			"anthropic_cache_read_and_creation",
			`{"usage":{"prompt_tokens":100,"completion_tokens":20,"cache_read_input_tokens":80,"cache_creation_input_tokens":60}}`,
			80, 60,
		},
		{
			"openai_legacy_no_creation",
			`{"usage":{"prompt_tokens":50,"completion_tokens":10,"prompt_tokens_details":{"cached_tokens":50}}}`,
			50, 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prompt, completion, _, read, creation := usageFromBody([]byte(tc.body), []byte(`{"model":"m"}`))
			if prompt <= 0 {
				t.Fatalf("expected non-zero prompt tokens, got %d", prompt)
			}
			if completion < 0 {
				t.Fatalf("expected non-negative completion, got %d", completion)
			}
			if read != tc.wantRead {
				t.Fatalf("cacheRead: want %d, got %d", tc.wantRead, read)
			}
			if creation != tc.wantCreation {
				t.Fatalf("cacheCreation: want %d, got %d", tc.wantCreation, creation)
			}
		})
	}
}

// ExtractNonStreaming is the public facade over usageFromBody. It must
// propagate the new cache_creation tuple value end-to-end so the
// pipeline can pass it to RecordFull.
func TestExtractNonStreamingCacheCreation(t *testing.T) {
	e := NewExtractor()
	resp := []byte(`{"usage":{"prompt_tokens":100,"completion_tokens":10,"prompt_tokens_details":{"cached_tokens":50,"cache_creation_input_tokens":30}}}`)
	prompt, completion, _, read, creation := e.ExtractNonStreaming(resp, []byte(`{"model":"m"}`))
	if prompt != 100 || completion != 10 {
		t.Fatalf("expected prompt=100 completion=10, got %d/%d", prompt, completion)
	}
	if read != 50 {
		t.Fatalf("expected read=50, got %d", read)
	}
	if creation != 30 {
		t.Fatalf("expected creation=30, got %d", creation)
	}
}

// Extractor.SnifferUsage must surface cacheCreation alongside the
// other fields so the streaming pipeline can pass it through. Uses
// real io.EOF so the sniffer drains its carry buffer.
func TestSnifferUsageCacheCreation(t *testing.T) {
	e := NewExtractor()
	body := `data: {"choices":[{"delta":{}}],"usage":{"prompt_tokens":100,"completion_tokens":10,"prompt_tokens_details":{"cached_tokens":50,"cache_creation_input_tokens":25}}}` + "\n\n"
	sniffer := e.NewSniffer(stringReader(body))
	if _, err := io.ReadAll(sniffer); err != nil {
		t.Fatalf("read: %v", err)
	}
	prompt, completion, read, creation := e.SnifferUsage(sniffer)
	if prompt != 100 || completion != 10 {
		t.Fatalf("expected prompt=100 completion=10, got %d/%d", prompt, completion)
	}
	if read != 50 {
		t.Fatalf("expected read=50, got %d", read)
	}
	if creation != 25 {
		t.Fatalf("expected creation=25, got %d", creation)
	}
}

type stringReaderImpl struct {
	s   string
	off int
}

func stringReader(s string) *stringReaderImpl { return &stringReaderImpl{s: s} }

func (r *stringReaderImpl) Read(p []byte) (int, error) {
	if r.off >= len(r.s) {
		return 0, io.EOF
	}
	n := copy(p, r.s[r.off:])
	r.off += n
	return n, nil
}
