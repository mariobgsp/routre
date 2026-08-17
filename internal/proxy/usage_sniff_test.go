package proxy

import (
	"io"
	"strings"
	"testing"
)

func readAllSniff(t *testing.T, s *usageSniffer) streamUsage {
	t.Helper()
	_, err := io.Copy(io.Discard, s)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	s.drainCarry()
	return s.usage()
}

// oneByteReader returns r but only one byte per Read, forcing token fields
// to be split across many Read calls (the streaming-realistic case).
type oneByteReader struct{ r io.Reader }

func (o oneByteReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	var b [1]byte
	n, err := o.r.Read(b[:])
	if n > 0 {
		p[0] = b[0]
	}
	return n, err
}

func TestUsageSnifferOpenAI(t *testing.T) {
	s := newUsageSniffer(strings.NewReader(
		`data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n" +
			`data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":8,"total_tokens":20}}` + "\n\n" +
			`data: [DONE]` + "\n\n"))
	u := readAllSniff(t, s)
	if u.prompt != 12 || u.completion != 8 {
		t.Fatalf("expected prompt=12 completion=8, got %+v", u)
	}
}

func TestUsageSnifferAnthropic(t *testing.T) {
	s := newUsageSniffer(strings.NewReader(
		`event: message_start` + "\n" + `data: {"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":0}}}` + "\n\n" +
			`event: message_delta` + "\n" + `data: {"type":"message_delta","usage":{"output_tokens":28}}` + "\n\n" +
			`event: message_stop` + "\n" + `data: {"type":"message_stop"}` + "\n\n"))
	u := readAllSniff(t, s)
	if u.prompt != 10 || u.completion != 28 {
		t.Fatalf("expected prompt=10 completion=28, got %+v", u)
	}
}

// Token numbers split mid-digit across Read boundaries must still be found.
func TestUsageSnifferSplitReads(t *testing.T) {
	s := newUsageSniffer(oneByteReader{strings.NewReader(
		`data: {"usage":{"prompt_tokens":123,"completion_tokens":456}}` + "\n\n")})
	u := readAllSniff(t, s)
	if u.prompt != 123 || u.completion != 456 {
		t.Fatalf("expected prompt=123 completion=456, got %+v", u)
	}
}
