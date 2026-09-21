// Package tokenize provides a lightweight token estimator used to measure
// token-usage reduction (RTK) and cache savings without invoking a real
// tokenizer. It is an approximation (roughly 4 bytes/token for mixed text)
// and is intended for before/after comparisons of the SAME payload, where
// systematic bias cancels out.
package tokenize

import (
	"sync/atomic"
	"unicode/utf8"
)

// ExactLimit is the body size below which a request-path caller may pay for
// an exact BPE count. Above it callers use a conservative estimate: a real
// BPE count costs hundreds of MB and most of a second per MiB, which is the
// single largest gateway-added cost on a tool-heavy request.
const ExactLimit = 64 << 10

// CountCapped returns an exact BPE count for text at or below ExactLimit and
// Estimate above it. Use it for observability counts on the request path
// (request logging, usage fallbacks, cache savings). Not for max_tokens
// sizing — see ClampCount.
func CountCapped(text string) int64 {
	if len(text) <= ExactLimit {
		return int64(Count(text, KindOpenAI))
	}
	return int64(Estimate(text))
}

// ClampCount returns an exact BPE count for text at or below ExactLimit and a
// deliberately PESSIMISTIC O(1) estimate above it: ceil(bytes/3)+1. It is the
// only count used to size max_tokens, where under-counting would let the
// gateway ask for a completion that cannot fit the upstream window (the
// upstream then rejects the whole request). Unlike Estimate (a documented ~4
// bytes/token), this over-counts JSON and code, and it does not allocate.
//
// The input is always a json.Marshal result, whose newlines are escaped, so
// the cheap bytes/3 bound is safely above Estimate for the same text.
func ClampCount(text string) int64 {
	if len(text) <= ExactLimit {
		return int64(Count(text, KindOpenAI))
	}
	return int64(len(text)+2)/3 + 1
}

// countObserver, when set, receives the byte length of every Count input.
// Request-path tests install one to prove no exact whole-body count runs;
// production never sets it, so the cost is one atomic load per Count.
var countObserver atomic.Pointer[func(int)]

// SetCountObserver installs (or clears, with nil) a test hook invoked by
// Count with the input length. It exists for the request-path guard test in
// internal/proxy and is not used in production.
func SetCountObserver(fn func(int)) {
	if fn == nil {
		countObserver.Store(nil)
		return
	}
	countObserver.Store(&fn)
}

// Estimate returns an approximate token count for the given text.
//
// Heuristic (documented approximation):
//   - ASCII runs: 1 token per 4 chars (OpenAI/Anthropic ballpark)
//   - non-ASCII (UTF-8) runes: 1 token per rune
//   - each newline counts 0.5 token (common in code/tool output)
//   - minimum 1 token for non-empty input
//
// This is a measurement tool for relative reduction, NOT a billing-grade
// tokenizer. Replace with tiktoken/claude-tokenizer when exact numbers are
// required.
func Estimate(text string) int {
	if text == "" {
		return 0
	}
	var ascii, other, newlines int
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		if r == '\n' {
			newlines++
		}
		if r < 0x80 {
			ascii++
		} else {
			other++
		}
		i += size
	}
	t := ascii/4 + other + (newlines+1)/2
	if t < 1 {
		t = 1
	}
	return t
}
