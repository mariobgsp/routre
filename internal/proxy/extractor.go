package proxy

import (
	"io"
)

// Extractor is the deep module that hides token extraction behind a small interface.
// It unifies non-streaming JSON parse and streaming SSE regex scan behind one seam.
type Extractor struct{}

// NewExtractor creates an Extractor.
func NewExtractor() *Extractor { return &Extractor{} }

// ExtractNonStreaming parses a non-streaming response body for usage.
// It handles OpenAI, Anthropic, and Gemini (via geminiToOpenAI unwrapping already done by caller).
func (e *Extractor) ExtractNonStreaming(respBody, reqBody []byte) (prompt, completion int64, cost float64, cacheRead int64) {
	return usageFromBody(respBody, reqBody)
}

// ExtractStreaming creates a sniffer that captures usage from a streaming body.
// Caller wraps the upstream Body with NewSniffer and reads through it; after the stream ends, call SnifferUsage.
func (e *Extractor) NewSniffer(r io.Reader) io.Reader {
	return newUsageSniffer(r)
}

// SnifferUsage extracts the captured usage from a sniffer after the stream ends.
func (e *Extractor) SnifferUsage(r io.Reader) (prompt, completion, cacheRead int64) {
	if s, ok := r.(*usageSniffer); ok {
		s.drainCarry()
		u := s.usage()
		return u.prompt, u.completion, u.cacheRead
	}
	return 0, 0, 0
}
