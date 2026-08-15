// Package tokenize provides a lightweight token estimator used to measure
// token-usage reduction (RTK) and cache savings without invoking a real
// tokenizer. It is an approximation (roughly 4 bytes/token for mixed text)
// and is intended for before/after comparisons of the SAME payload, where
// systematic bias cancels out.
package tokenize

import "unicode/utf8"

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
