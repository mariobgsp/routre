package router

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrClass classifies an upstream failure for cooldown policy.
type ErrClass int

const (
	// ErrNetwork: connection/read errors, TLS failures, timeouts.
	ErrNetwork ErrClass = iota
	// ErrTimeout: explicit deadline exceeded.
	ErrTimeout
	// ErrRateLimit: 429 (and provider-side throttling).
	ErrRateLimit
	// ErrAuth: 401/403.
	ErrAuth
	// ErrServer: 5xx.
	ErrServer
	// ErrClient: 4xx other than auth/rate-limit (400, 404, 422...).
	ErrClient
	// ErrCredits: 402, or a credits/billing error surfaced as 401
	// (opencode-zen returns 401 CreditsError). Retryable in the sense that
	// the next candidate should be tried, but the provider itself is NOT
	// put into cooldown — it is a billing state, not an outage, and the
	// provider may still serve its free variants.
	ErrCredits
	// ErrStream: failure after the first stream byte — the client already
	// received a partial response; retrying would duplicate output.
	ErrStream
	// ErrOverloaded: 5xx (or 429) with a body that names
	// "overloaded" / "capacity" / "temporarily unavailable". The
	// provider is healthy but temporarily out of capacity. Fail over
	// to the next candidate; do NOT escalate cooldown — a single
	// overloaded response must not lock the provider out for minutes.
	// The upstream's Retry-After (when present) is still respected,
	// but capped at 30s so a long Retry-After doesn't deny service.
	ErrOverloaded
	// ErrConfig: a deterministic gateway-configuration error (e.g. a provider
	// key env var that is not set). No cooldown and no same-candidate retry:
	// retrying cannot fix a typo, and escalating would lock every provider out
	// for minutes on a config mistake.
	ErrConfig
)

// ErrMissingProviderKey marks a provider whose API key env var is unset. It is
// a gateway misconfiguration, not an upstream failure, so it must never put a
// provider into cooldown.
var ErrMissingProviderKey = errors.New("provider key is not set")

var errClassNames = map[ErrClass]string{
	ErrNetwork:    "network",
	ErrTimeout:    "timeout",
	ErrRateLimit:  "rate-limit",
	ErrAuth:       "auth",
	ErrServer:     "server",
	ErrClient:     "client",
	ErrCredits:    "credits",
	ErrStream:     "stream",
	ErrOverloaded: "overloaded",
	ErrConfig:     "config",
}

func (c ErrClass) String() string {
	if s, ok := errClassNames[c]; ok {
		return s
	}
	return fmt.Sprintf("ErrClass(%d)", int(c))
}

// CooldownPolicy governs backoff growth.
type CooldownPolicy struct {
	Base    time.Duration // first cooldown
	Max     time.Duration // cap
	MaxHits int           // failures before the cap is reached
}

// DefaultCooldownPolicy mirrors 9router's error-config: 2s base,
// exponential, 5min cap.
//
// ponytail: 5min chosen because real-provider transient overloads (Anthropic
// 529, OpenAI 5xx) clear in seconds-to-minutes; a 30min cap turned a
// 60-second blip into a half-hour outage. 5min still absorbs a real sustained
// outage (the exponential backoff saturates by hit 9: 2*2^8=512s≈8.5m →
// clamped to 5m), but recovers faster once the provider is healthy.
func DefaultCooldownPolicy() CooldownPolicy {
	return CooldownPolicy{Base: 2 * time.Second, Max: 5 * time.Minute, MaxHits: 30}
}

// Classify maps an error to a class.
func Classify(err error) ErrClass {
	if err == nil {
		return ErrClient
	}
	if errors.Is(err, errMidStream) {
		return ErrStream
	}
	if errors.Is(err, ErrMissingProviderKey) {
		return ErrConfig
	}
	if errors.Is(err, contextDeadlineExceeded) {
		return ErrTimeout
	}
	return ErrNetwork
}

// ClassifyStatus maps an HTTP status to a class.
func ClassifyStatus(status int) ErrClass {
	switch {
	case status == 401 || status == 403:
		return ErrAuth
	case status == 402:
		return ErrCredits
	case status == 429:
		return ErrRateLimit
	case status >= 500:
		return ErrServer
	case status >= 400:
		return ErrClient
	default:
		return ErrClient
	}
}

// ClassifyStatusBody maps an HTTP status plus a response body to a class,
// refining 401s that are actually billing failures (opencode-zen returns
// 401 CreditsError when the account balance is exhausted), 401s/403s that
// actually say the MODEL is unknown (an auth-shaped status for a
// model-level problem), and 5xx/429 that are actually transient
// "overloaded" responses (Anthropic 529 with overloaded_error, OpenAI
// "model is currently overloaded", etc.).
func ClassifyStatusBody(status int, body []byte) ErrClass {
	c := ClassifyStatus(status)
	if (c == ErrAuth || c == ErrCredits) && bodySaysModelUnknown(body) {
		// The rejection is about the model ("does not exist", "not a
		// valid model"), not the key or the balance: no credential
		// refresh or same-cand retry can make an unserved model work.
		// ErrClient makes the runner fail over without the auth
		// machinery and lets the all-client reshape surface a terminal
		// model_not_found (404) instead of a retryable-looking 502/503.
		return ErrClient
	}
	if c == ErrAuth && bodyHasCredits(body) {
		return ErrCredits
	}
	// 529 is Anthropic's dedicated capacity code; treat all of them as
	// transient overload regardless of body shape.
	if status == 529 {
		return ErrOverloaded
	}
	if (c == ErrServer || c == ErrRateLimit) && bodyHasOverloaded(body) {
		return ErrOverloaded
	}
	return c
}

// containsAny reports whether body (lowercased, capped at 4KiB) contains any
// of the given lowercase phrases.
func containsAny(body []byte, phrases []string) bool {
	if len(body) > 4096 {
		body = body[:4096]
	}
	low := strings.ToLower(string(body))
	for _, p := range phrases {
		if strings.Contains(low, p) {
			return true
		}
	}
	return false
}

// bodyHasCredits reports whether an error body mentions a balance/credits problem.
func bodyHasCredits(body []byte) bool {
	return containsAny(body, []string{"credit", "insufficient balance", "balance"})
}

// modelUnknownPhrases are high-precision markers that an error body is
// rejecting the MODEL (unknown / unsupported / invalid / nonexistent), not
// the key or the balance. Deliberately narrow: an auth body that merely
// mentions "model" in passing never matches. Evaluated on 401/403/402 only.
var modelUnknownPhrases = []string{
	"not exist", // DeepSeek "Model Not Exist", OpenAI "does not exist", Spark, iFlytek
	"model not found",
	"model not_found",
	"model_not_found",
	"not a valid model",
	"invalid model",
	"unknown model",
	"model not supported",
	"model is not supported",
	"is not supported", // "Model <name> is not supported" (name between words); covers opencode-zen 401
	"model not available",
	"does not support model",
	"no endpoints found for the model",
}

// bodySaysModelUnknown reports whether an error body names an unknown or
// unsupported model. Deliberately narrow: see modelUnknownPhrases.
func bodySaysModelUnknown(body []byte) bool {
	return containsAny(body, modelUnknownPhrases)
}

// bodyHasOverloaded reports whether an error body describes transient capacity trouble.
func bodyHasOverloaded(body []byte) bool {
	return containsAny(body, []string{
		"overloaded", "temporarily unavailable", "capacity",
		"try again later", "rate limit reached", "rate_limit", "too many requests",
	})
}

// IsRetryableClass reports whether a failure should escalate cooldown and
// trigger failover. Stream aborts and client-caused 4xx do not. Credits
// failures fail over but never escalate cooldown (see ReportFailure).
// Overloaded errors fail over with a short Retry-After but do not
// stack the gateway's own exponential backoff. Config errors are terminal
// for the candidate: they fail over without a cooldown or a retry.
func IsRetryableClass(c ErrClass) bool {
	switch c {
	case ErrNetwork, ErrTimeout, ErrRateLimit, ErrAuth, ErrServer, ErrCredits, ErrOverloaded:
		return true
	default:
		return false
	}
}
