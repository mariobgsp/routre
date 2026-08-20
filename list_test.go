package main

import (
	"bytes"
	"strings"
	"testing"

	"routre-cli/internal/usage"
)

func TestParseRow(t *testing.T) {
	got := parseRow(map[string]any{
		"provider": "p", "model": "m",
		"prompt_tokens": float64(10), "completion_tokens": float64(5),
		"rtk_saved_tokens": float64(2), "cache_saved_tokens": float64(1),
		"cost_usd": float64(0.001), "saved_usd": float64(0.0005), "requests": float64(3),
	})
	if got.Provider != "p" || got.PromptTokens != 10 || got.CompletionTokens != 5 ||
		got.RTKSavedTokens != 2 || got.CacheSavedTokens != 1 ||
		got.CostUSD != 0.001 || got.SavedUSD != 0.0005 || got.Requests != 3 {
		t.Fatalf("parseRow = %+v", got)
	}
}

func TestPct(t *testing.T) {
	if got := pct(90, 100); got != 90 {
		t.Fatalf("pct(90,100) = %v", got)
	}
	if got := pct(10, 0); got != 0 {
		t.Fatalf("pct(10,0) = %v, want 0 (no divide-by-zero)", got)
	}
}

func TestPrintUsageTo(t *testing.T) {
	rows := []usage.Row{{
		Provider: "codex", Model: "m",
		PromptTokens: 10, CompletionTokens: 5,
		RTKSavedTokens: 3, CacheSavedTokens: 1,
		CostUSD: 0.0001, SavedUSD: 0.00001, Requests: 2,
	}}
	var buf bytes.Buffer
	printUsageTo(&buf, rows, true)
	out := buf.String()
	for _, want := range []string{
		"source: live",
		"codex",
		"requests: 2",
		"consumed: 15 tokens (10 in + 5 out)",
		"TOTAL",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q\n%s", want, out)
		}
	}
}

func TestPrintUsageToNoTraffic(t *testing.T) {
	var buf bytes.Buffer
	printUsageTo(&buf, nil, false)
	if !strings.Contains(buf.String(), "no traffic yet") {
		t.Fatalf("expected 'no traffic yet' message, got %q", buf.String())
	}
}
