package proxy

import (
	"encoding/json"
	"net/http"
	"strings"

	"routre-cli/internal/config"
	"routre-cli/internal/tokenize"
	"routre-cli/internal/usage"
)

// clientName attributes a request to the coding agent that sent it, by
// sniffing the User-Agent header. Unknown clients are grouped as
// "unknown". Vendors are normalized to lowercase without version noise.
func clientName(r *http.Request) string {
	ua := strings.ToLower(r.Header.Get("User-Agent"))
	switch {
	case strings.Contains(ua, "opencode"):
		return "opencode"
	case strings.Contains(ua, "claude"):
		return "claude-code"
	case strings.Contains(ua, "codex"):
		return "codex"
	case strings.Contains(ua, "cursor"):
		return "cursor"
	case strings.Contains(ua, "cline"):
		return "cline"
	case strings.Contains(ua, "continue"):
		return "continue"
	case ua != "":
		return ua
	default:
		return "unknown"
	}
}

// modelFromBody extracts the requested model from a request body.
func modelFromBody(body []byte) string {
	var doc struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &doc); err != nil || doc.Model == "" {
		return ""
	}
	return doc.Model
}

// usageFromBody parses provider-reported token usage and cost out of a
// non-streaming upstream response. Falls back to length-based estimates
// when the provider did not report usage. OpenRouter reports a `cost`
// field; when present it is used verbatim.
func usageFromBody(respBody, reqBody []byte) (prompt, completion int64, cost float64) {
	var doc struct {
		Usage struct {
			PromptTokens     int64   `json:"prompt_tokens"`
			CompletionTokens int64   `json:"completion_tokens"`
			Cost             float64 `json:"cost"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(respBody, &doc); err == nil && doc.Usage.PromptTokens > 0 {
		return doc.Usage.PromptTokens, doc.Usage.CompletionTokens, doc.Usage.Cost
	}
	return int64(tokenize.Estimate(string(reqBody))), 0, 0
}

// pricesOf returns the configured prices for a provider by name.
func pricesOf(c config.Config, providerName string) usage.Prices {
	for _, t := range c.Tiers {
		for _, p := range t.Providers {
			if p.Name == providerName {
				return usage.Prices{InputPerMillion: p.PriceIn, OutputPerMillion: p.PriceOut}
			}
		}
	}
	return usage.Prices{}
}
