package proxy

import (
	"bytes"
	"io"
	"regexp"
	"strconv"
)

// streamUsage holds token counts captured from a streaming response.
type streamUsage struct {
	prompt     int64
	completion int64
	// cacheRead: provider-reported prompt-cache hit tokens (OpenAI
	// `cached_tokens`, Anthropic `cache_read_input_tokens`), billed at the
	// discounted cache-read rate. Zero when the provider does not report
	// them or the prefix was not cached.
	cacheRead int64
}

// usageSniffer is a pass-through io.Reader that sits in front of an upstream
// SSE body and captures usage/progress token counts as bytes flow by. It does
// not alter the byte stream (proxy must stay byte-for-byte for same-kind
// relays). It scans SSE data lines for the token fields used by each dialect:
//
//	OpenAI:    "prompt_tokens":N, "completion_tokens":N
//	Anthropic: "input_tokens":N (message_start), "output_tokens":N (message_delta)
//
// A per-line carry handles fields split across Read boundaries. Token counts
// are best-effort: providers emit usage on the final chunk (OpenAI) or across
// message_start/message_delta events (Anthropic).
type usageSniffer struct {
	r          io.Reader
	carry      []byte
	prompt     int64
	completion int64
	cacheRead  int64
}

var (
	rePrompt     = regexp.MustCompile(`"(?:prompt_tokens|input_tokens)":\s*(\d+)`)
	reCompletion = regexp.MustCompile(`"(?:completion_tokens|output_tokens)":\s*(\d+)`)
	// cached_tokens (OpenAI/OpenRouter), cache_read_input_tokens (Anthropic)
	// and cachedContentTokenCount (Gemini) report prompt-cache hits. Mirrors
	// usageFromBody, which already parses the first two non-streaming.
	reCached = regexp.MustCompile(`"(?:cached_tokens|cache_read_input_tokens|cachedContentTokenCount)":\s*(\d+)`)
)

func newUsageSniffer(r io.Reader) *usageSniffer { return &usageSniffer{r: r} }

func (u *usageSniffer) Read(p []byte) (int, error) {
	n, err := u.r.Read(p)
	if n > 0 {
		// Scan complete lines in this chunk plus any carry from prior reads.
		u.carry = append(u.carry, p[:n]...)
		// Find up to the last newline so we only parse complete lines.
		last := 0
		for i, b := range u.carry {
			if b == '\n' {
				last = i + 1
			}
		}
		if last > 0 {
			chunk := u.carry[:last]
			// Gate: every matched field contains "token"/"Token"; skip the
			// three regexes entirely on frames that cannot match (the common
			// case — content deltas).
			if bytes.Contains(chunk, []byte("token")) || bytes.Contains(chunk, []byte("Token")) {
				u.scan(chunk)
			}
			u.carry = append([]byte(nil), u.carry[last:]...)
		}
	}
	return n, err
}

func (u *usageSniffer) scan(b []byte) {
	// Take the LAST occurrence: Anthropic emits output_tokens on both
	// message_start (0) and message_delta (real value); OpenAI collapses
	// usage onto the final chunk, so the last is correct there too.
	if ms := reCompletion.FindAllSubmatch(b, -1); len(ms) > 0 {
		if v, e := strconv.ParseInt(string(ms[len(ms)-1][1]), 10, 64); e == nil {
			u.completion = v
		}
	}
	if ms := rePrompt.FindAllSubmatch(b, -1); len(ms) > 0 {
		if v, e := strconv.ParseInt(string(ms[len(ms)-1][1]), 10, 64); e == nil {
			u.prompt = v
		}
	}
	if ms := reCached.FindAllSubmatch(b, -1); len(ms) > 0 {
		if v, e := strconv.ParseInt(string(ms[len(ms)-1][1]), 10, 64); e == nil {
			u.cacheRead = v
		}
	}
}

func (u *usageSniffer) usage() streamUsage {
	return streamUsage{prompt: u.prompt, completion: u.completion, cacheRead: u.cacheRead}
}

// drainCarry scans any remaining buffered carry (stream ended without a
// trailing newline on the last usage line).
func (u *usageSniffer) drainCarry() {
	if len(u.carry) > 0 {
		u.scan(u.carry)
		u.carry = nil
	}
}
