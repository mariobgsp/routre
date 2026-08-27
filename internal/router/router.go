// Package router implements tiered provider selection with automatic
// failover and per-provider failure cooldowns (exponential backoff).
//
// Design (from research):
//   - tiers are tried in order (subscription -> cheap -> free);
//   - within a tier, providers are tried in order;
//   - a provider in cooldown is skipped;
//   - on failure, the provider's cooldown grows exponentially (2s base,
//     capped at 30min);
//   - only hard failures escalate cooldown; mid-stream client aborts do not.
package router

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/mariobgsp/routre/internal/config"
	"time"

	"github.com/mariobgsp/routre/internal/tokenize"
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
)

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
// 401 CreditsError when the account balance is exhausted) and 5xx/429
// that are actually transient "overloaded" responses (Anthropic 529 with
// overloaded_error, OpenAI "model is currently overloaded", etc.).
func ClassifyStatusBody(status int, body []byte) ErrClass {
	c := ClassifyStatus(status)
	if c == ErrAuth && bodyHasCredits(body) {
		return ErrCredits
	}
	if status == 529 && len(body) == 0 {
		return ErrOverloaded
	}
	if (c == ErrServer || c == ErrRateLimit) && bodyHasOverloaded(body) {
		return ErrOverloaded
	}
	return c
}

// bodyHasCredits reports whether an error body mentions a balance/credits
// problem ("insufficient balance", "CreditsError", "credits").
func bodyHasCredits(body []byte) bool {
	low := strings.ToLower(string(body))
	return strings.Contains(low, "credit") || strings.Contains(low, "insufficient balance") || strings.Contains(low, "balance")
}

// bodyHasOverloaded reports whether an error body describes a transient
// capacity problem. Anthropic 529 ("overloaded_error"), OpenAI 5xx
// ("model is currently overloaded"), and similar. These get a
// dedicated class so the gateway's exponential backoff does not lock
// the provider out for minutes on a single blip.
func bodyHasOverloaded(body []byte) bool {
	low := strings.ToLower(string(body))
	return strings.Contains(low, "overloaded") ||
		strings.Contains(low, "temporarily unavailable") ||
		strings.Contains(low, "capacity") ||
		strings.Contains(low, "try again later") ||
		strings.Contains(low, "rate limit reached") ||
		strings.Contains(low, "rate_limit") ||
		strings.Contains(low, "too many requests")
}

// IsRetryableClass reports whether a failure should escalate cooldown and
// trigger failover. Stream aborts and client-caused 4xx do not. Credits
// failures fail over but never escalate cooldown (see ReportFailure).
// Overloaded errors fail over with a short Retry-After but do not
// stack the gateway's own exponential backoff.
func IsRetryableClass(c ErrClass) bool {
	switch c {
	case ErrNetwork, ErrTimeout, ErrRateLimit, ErrAuth, ErrServer, ErrCredits, ErrOverloaded:
		return true
	default:
		return false
	}
}

// ProviderInput is the public shape accepted by New.
type ProviderInput struct {
	Name      string
	Kind      string
	BaseURL   string
	APIKeyEnv string
	Models    []string
	// MaxTokens: ceiling applied to max_tokens on relay (0 = no clamp).
	MaxTokens int64
}

// TierInput is the public shape accepted by New.
type TierInput struct {
	Name      string
	Providers []ProviderInput
}

// ProviderState is the runtime state of one configured provider.
type ProviderState struct {
	Provider ProviderInfo
	failures int
	until    time.Time // cooldown end; zero = not in cooldown
}

// ProviderInfo is the static configuration of a provider.
type ProviderInfo struct {
	Name      string
	Kind      string
	BaseURL   string
	APIKeyEnv string
	Models    []string
	Tier      string
	TierIndex int
	// MaxTokens: ceiling applied to max_tokens on relay (0 = no clamp).
	MaxTokens int64
}

// Router is a concurrency-safe tiered provider list with cooldowns.
type Router struct {
	mu     sync.RWMutex
	provs  []*ProviderState // flattened in tier order
	policy CooldownPolicy
	now    func() time.Time // clock injection for tests
	// forwardUnknown: when true, a model absent from every provider's
	// whitelist is still forwarded verbatim to all available providers
	// (tier order, failover). Enables zero-config handling of new/future
	// models. Set via SetForwardUnknown.
	forwardUnknown bool
}

// CandidatesWithFallbacks returns Candidates(model), then the candidates
// of each fallback model in order, deduplicated by provider. A provider
// tried for the requested model is never tried again for a fallback.
func (r *Router) CandidatesWithFallbacks(model string, fallbacks []string) []Candidate {
	out := r.Candidates(model)
	seen := map[string]bool{}
	for _, c := range out {
		seen[c.Provider.Provider.Name] = true
	}
	for _, f := range fallbacks {
		if f == "" || f == model {
			continue
		}
		for _, c := range r.Candidates(f) {
			if seen[c.Provider.Provider.Name] {
				continue
			}
			seen[c.Provider.Provider.Name] = true
			out = append(out, c)
		}
	}
	return out
}

// Candidate is a provider eligible to serve a requested model, with the
// concrete upstream model name to send (may be a free variant).
type Candidate struct {
	Provider *ProviderState
	Upstream string // model name to send upstream (free variant or original)
	IsFree   bool
	// IsWildcard: the provider does not whitelist this model; the request
	// was forwarded verbatim under forward_unknown. A client-error rejection
	// (400/404) from a wildcard candidate means "this provider lacks the
	// model", not "the request is bad" — so the gateway fails over to the
	// next candidate instead of surfacing the first rejection.
	IsWildcard bool
}

// Payload returns the wire payload for this candidate, with the model
// rewritten to the upstream name and MaxTokens clamped. It is the deep
// module's hidden behavior: callers pass the processed body and get back
// ready-to-send bytes, without knowing about provider-qualified names or
// free variants. Fail-open: malformed JSON passes through unchanged.
func (c Candidate) Payload(processed []byte, requested string) []byte {
	// Fast path: no mutation needed
	if c.Upstream == requested && c.Provider.Provider.MaxTokens == 0 {
		return processed
	}
	var doc map[string]any
	if err := json.Unmarshal(processed, &doc); err != nil {
		return processed
	}
	if c.Upstream != requested {
		doc["model"] = c.Upstream
	}
	clampMaxTokens(doc, c.Provider.Provider.MaxTokens)
	out, err := json.Marshal(doc)
	if err != nil {
		return processed
	}
	return out
}

// ShouldFailoverOnClientError reports whether a client-error (400/404)
// from this candidate should be treated as "try next provider" rather
// than surfacing the error. True only for wildcard-forwarded models.
func (c Candidate) ShouldFailoverOnClientError() bool { return c.IsWildcard }

// clampMaxTokens caps max_tokens in doc to fit the provider's ceiling.
// Copied from proxy for locality; see proxy.clampMaxTokens for contract.
func clampMaxTokens(doc map[string]any, ceiling int64) {
	if ceiling <= 0 {
		return
	}
	mt, ok := doc["max_tokens"]
	if !ok {
		return
	}
	var n int64
	switch v := mt.(type) {
	case float64:
		n = int64(v)
	case json.Number:
		n, _ = v.Int64()
	default:
		return
	}
	promptEst := int64(0)
	if msgs, ok := doc["messages"]; ok {
		if mb, err := json.Marshal(msgs); err == nil {
			promptEst = int64(tokenize.Count(string(mb), tokenize.KindOpenAI))
		}
	}
	const margin = 512
	maxAllowed := ceiling - promptEst - margin
	if maxAllowed < 1024 {
		maxAllowed = 1024
	}
	if n <= maxAllowed {
		return
	}
	doc["max_tokens"] = maxAllowed
}

// stripProviderPrefix removes the leading "provider/" label from a client
// model reference when (and only when) the first segment exactly equals the
// provider's configured name. Otherwise it returns "" and the model is
// passed through unchanged. The prefix is a client-side routing construct
// (opencode splits at the FIRST "/" and rejoins the remainder), so the
// upstream must always receive the bare listed name — including models whose
// IDs legitimately contain further slashes (openrouter/openai/gpt-5.6-luna
// -> openai/gpt-5.6-luna). Never tail-after-last-slash: that turns valid
// multi-segment IDs like openai/gpt-5.6-luna into garbage.
func stripProviderPrefix(provider, model string) string {
	if provider == "" || model == provider+"/" {
		return ""
	}
	prefix := provider + "/"
	if strings.HasPrefix(model, prefix) {
		return strings.TrimPrefix(model, prefix)
	}
	return ""
}

// listedName returns the model name from a provider's list that should be
// sent upstream. A client may qualify the model with a provider prefix
// ("opencode-go/gpt-5.6-luna"); the upstream must receive the bare listed
// name ("gpt-5.6-luna"), not the prefixed client string. Exact match on the
// full name wins; otherwise the provider-prefixed form is unwrapped; the
// requested name is returned unchanged as the last resort.
func listedName(models []string, provider, model string) string {
	tail := stripProviderPrefix(provider, model)
	for _, m := range models {
		if m == model || (tail != "" && m == tail) {
			return m
		}
	}
	return model
}

// freeVariantOf returns the free variant name of a model for a provider's
// model list, or "" when none exists. OpenCode-style: "m" -> "m-free";
// OpenRouter-style: "org/m" -> "org/m:free". Matching is tolerant of
// provider qualification on either side: client "m" matches provider
// "org/m:free", and client "org/m" matches provider "org/m:free".
func freeVariantOf(models []string, model string) string {
	tail := model
	if i := strings.LastIndex(model, "/"); i >= 0 {
		tail = model[i+1:]
	}
	for _, m := range models {
		base := m
		switch {
		case strings.HasSuffix(m, ":free"):
			base = strings.TrimSuffix(m, ":free")
		case strings.HasSuffix(m, "-free"):
			base = strings.TrimSuffix(m, "-free")
		default:
			continue
		}
		if base == model || base == tail {
			return m
		}
		if i := strings.LastIndex(base, "/"); i >= 0 && base[i+1:] == tail {
			return m
		}
	}
	return ""
}

// providerServes reports whether the provider's model list includes model
// (exact match, or provider-qualified client model "provider/model" where
// the first segment matches this provider's name).
func providerServes(models []string, provider, model string) bool {
	for _, m := range models {
		if m == model {
			return true
		}
	}
	tail := stripProviderPrefix(provider, model)
	if tail == "" {
		return false
	}
	for _, m := range models {
		if m == tail {
			return true
		}
	}
	return false
}

// Candidates returns every provider (in tier order, cooldown respected)
// that can serve the requested model, with the upstream model name to use.
//
// Routing contract (README: "Provider-qualified model names"):
//   - "<provider>/<model>" is a client-side routing label only. When the
//     first path segment exactly matches a configured provider name, that
//     provider is selected directly and the remainder is forwarded verbatim
//     as the upstream model — no whitelist check. This makes the gateway a
//     dumb forwarder for qualified names so new upstream models (e.g.
//     opencode-go/muse-spark-1.2-contributor) work without a config edit.
//   - Bare model names ("muse-spark-1.2") still use the whitelist: the
//     provider must list the model or a free variant of it. This preserves
//     the OpenRouter 402-cascade guard (don't ask a provider for a model it
//     has never advertised).
//
// Free variants are preferred over the paid model when the request is
// unqualified: a provider listing "m-free" serves "m" requests via the
// free variant first.
func (r *Router) Candidates(model string) []Candidate {
	r.mu.RLock()
	defer r.mu.RUnlock()
	now := r.now()
	var out []Candidate
	for _, p := range r.provs {
		if now.Before(p.until) {
			continue
		}
		// Explicit provider-qualified routing: "opencode-go/muse-spark-1.2-contributor"
		// -> provider opencode-go, upstream muse-spark-1.2-contributor. Forward
		// verbatim regardless of the configured Models list (gateway is a
		// forwarder; upstream is authoritative). Honors cooldown already checked.
		if tail := stripProviderPrefix(p.Provider.Name, model); tail != "" {
			isFree := strings.HasSuffix(tail, ":free") || strings.HasSuffix(tail, "-free")
			out = append(out, Candidate{Provider: p, Upstream: tail, IsFree: isFree})
			continue
		}
		if providerServes(p.Provider.Models, p.Provider.Name, model) {
			// Prefer the free variant when the provider has one.
			if fv := freeVariantOf(p.Provider.Models, model); fv != "" {
				out = append(out, Candidate{Provider: p, Upstream: fv, IsFree: true})
				continue
			}
			out = append(out, Candidate{Provider: p, Upstream: listedName(p.Provider.Models, p.Provider.Name, model), IsFree: false})
			continue
		}
		// Provider does not list the model but has a free variant of it.
		if fv := freeVariantOf(p.Provider.Models, model); fv != "" {
			out = append(out, Candidate{Provider: p, Upstream: fv, IsFree: true})
		}
	}
	if len(out) == 0 && r.forwardUnknown {
		// Unknown/future model: forward verbatim to every available
		// provider in tier order so the request is attempted (and fails
		// over automatically). This is the zero-config path — a model that
		// appears upstream works immediately, no whitelist edit needed.
		// A 402/404 from one provider cascades to the next. Cooldowns are
		// still respected (a provider in cooldown is skipped).
		for _, p := range r.provs {
			if now.Before(p.until) {
				continue
			}
			out = append(out, Candidate{Provider: p, Upstream: model, IsFree: false, IsWildcard: true})
		}
	}
	return out
}

// New builds a Router from the config tiers.
func New(tiers []TierInput, policy CooldownPolicy) *Router {
	r := &Router{policy: policy, now: time.Now}
	for ti, t := range tiers {
		for _, p := range t.Providers {
			r.provs = append(r.provs, &ProviderState{
				Provider: ProviderInfo{
					Name:      p.Name,
					Kind:      p.Kind,
					BaseURL:   p.BaseURL,
					APIKeyEnv: p.APIKeyEnv,
					Models:    p.Models,
					Tier:      t.Name,
					TierIndex: ti,
					MaxTokens: p.MaxTokens,
				},
			})
		}
	}
	return r
}

// Next returns the next usable provider starting at index >= from,
// respecting cooldowns. Returns nil when none is available.
// from is the index after the previously tried provider.
func (r *Router) Next(from int) *ProviderState {
	r.mu.RLock()
	defer r.mu.RUnlock()
	now := r.now()
	for i := from; i < len(r.provs); i++ {
		if now.Before(r.provs[i].until) {
			continue
		}
		return r.provs[i]
	}
	return nil
}

// AllAvailable returns every provider currently not in cooldown (for
// status/health reporting).
func (r *Router) AllAvailable() []*ProviderState {
	r.mu.RLock()
	defer r.mu.RUnlock()
	now := r.now()
	var out []*ProviderState
	for _, p := range r.provs {
		if !now.Before(p.until) {
			out = append(out, p)
		}
	}
	return out
}

// ReportSuccess resets a provider's failure count and cooldown.
func (r *Router) ReportSuccess(p *ProviderState) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p.failures = 0
	p.until = time.Time{}
}

// ReportFailure records a failure and computes the next cooldown window.
// Stream aborts (ErrStream) never escalate. Credits failures (ErrCredits)
// also never escalate: the provider is out of money, not out of service —
// it may still serve free variants, so it must not be cooldowned.
func (r *Router) ReportFailure(p *ProviderState, class ErrClass) {
	r.reportFailure(p, class, 0)
}

// ReportFailureWithBackoff records a failure and computes the next cooldown
// window, but never lets it end before the server-mandated retryAfter delay
// (an upstream Retry-After header). Unlike the exponential backoff, this is
// data from the provider telling us when it will accept traffic again, so a
// Retry-After of e.g. 30s on a low base takes priority over a 2s default.
func (r *Router) ReportFailureWithBackoff(p *ProviderState, class ErrClass, retryAfter time.Duration) {
	r.reportFailure(p, class, retryAfter)
}

func (r *Router) reportFailure(p *ProviderState, class ErrClass, retryAfter time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Stream aborts and billing failures do not put the provider in
	// cooldown — the provider is fine, the user's session is the
	// problem. ErrOverloaded is the same shape: the provider is
	// healthy but out of capacity. We honor the upstream's
	// Retry-After (capped at 30s) so we don't hammer them back into
	// overload, but we do NOT stack our own exponential backoff.
	if class == ErrStream || class == ErrCredits {
		return
	}
	if class == ErrOverloaded {
		// Honor the upstream's Retry-After but cap at 30s. Reset
		// the failure counter so a single overloaded blip does
		// not stack with earlier real failures.
		if retryAfter > 30*time.Second {
			retryAfter = 30 * time.Second
		}
		if retryAfter < r.policy.Base {
			retryAfter = r.policy.Base
		}
		p.until = r.now().Add(retryAfter)
		p.failures = 0
		return
	}
	if p.failures < r.policy.MaxHits {
		p.failures++
	}
	n := p.failures
	// 2^n * base, clamped.
	d := r.policy.Base
	for i := 1; i < n; i++ {
		if d >= r.policy.Max/2 {
			d = r.policy.Max
			break
		}
		d *= 2
	}
	if d > r.policy.Max {
		d = r.policy.Max
	}
	if retryAfter > d {
		d = retryAfter
	}
	if d > r.policy.Max {
		d = r.policy.Max
	}
	p.until = r.now().Add(d)
}

// CooldownRemaining reports how long p stays in cooldown (0 if none).
func (r *Router) CooldownRemaining(p *ProviderState) time.Duration {
	r.mu.RLock()
	defer r.mu.RUnlock()
	now := r.now()
	if now.Before(p.until) {
		return p.until.Sub(now)
	}
	return 0
}

// Status is a snapshot of every provider for /v1/status.
type Status struct {
	Provider          string
	Tier              string
	TierIndex         int
	Kind              string
	Models            []string
	Failures          int
	CooldownRemaining time.Duration
}

// Status returns a snapshot of all providers.
func (r *Router) Status() []Status {
	r.mu.RLock()
	defer r.mu.RUnlock()
	now := r.now()
	out := make([]Status, 0, len(r.provs))
	for _, p := range r.provs {
		cd := time.Duration(0)
		if now.Before(p.until) {
			cd = p.until.Sub(now)
		}
		out = append(out, Status{
			Provider:          p.Provider.Name,
			Tier:              p.Provider.Tier,
			TierIndex:         p.Provider.TierIndex,
			Kind:              p.Provider.Kind,
			Models:            append([]string(nil), p.Provider.Models...),
			Failures:          p.failures,
			CooldownRemaining: cd,
		})
	}
	return out
}

// ServesModel reports whether any provider (regardless of cooldown)
// could serve model. Qualified "<provider>/<model>" is always considered
// served when the provider exists (gateway is a forwarder; upstream is
// authoritative). Bare names still require a whitelist hit or free variant.
func (r *Router) ServesModel(model string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, p := range r.provs {
		if stripProviderPrefix(p.Provider.Name, model) != "" {
			return true
		}
		if providerServes(p.Provider.Models, p.Provider.Name, model) {
			return true
		}
		if freeVariantOf(p.Provider.Models, model) != "" {
			return true
		}
	}
	return false
}

// MinCooldownForModel returns the shortest cooldown remaining among
// providers that could serve model (or a free variant of it), and whether
// at least one such provider exists. Qualified "<provider>/<model>" counts
// as served by that provider (forwarder contract). Callers use it to set
// Retry-After when every candidate is cooling down.
func (r *Router) MinCooldownForModel(model string, forwardUnknown bool) (time.Duration, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	now := r.now()
	best := time.Duration(0)
	found := false
	for _, p := range r.provs {
		qualified := stripProviderPrefix(p.Provider.Name, model) != ""
		serves := qualified || providerServes(p.Provider.Models, p.Provider.Name, model) || freeVariantOf(p.Provider.Models, model) != ""
		// Under forward_unknown every provider is a potential server of an
		// unlisted model, so cooldown (not the whitelist) decides identity.
		if !serves && !forwardUnknown {
			continue
		}
		found = true
		if now.Before(p.until) {
			rem := p.until.Sub(now)
			if best == 0 || rem < best {
				best = rem
			}
		}
	}
	return best, found
}

// Reset replaces the provider list and policy (config reload). The old
// failure state is discarded; cooldowns restart fresh.
func (r *Router) Reset(tiers []TierInput, policy CooldownPolicy) {
	newR := New(tiers, policy)
	r.mu.Lock()
	r.provs = newR.provs
	r.policy = newR.policy
	r.mu.Unlock()
}

// Policy returns the cooldown policy (used when rebuilding the router on
// config reload).
func (r *Router) Policy() CooldownPolicy {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.policy
}

// Len returns the total number of providers.
func (r *Router) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.provs)
}

// SetForwardUnknown toggles zero-config forwarding of unknown/future
// models. Threaded from config (defaults true). Reset preserves it because
// Reset mutates the existing Router in place.
func (r *Router) SetForwardUnknown(v bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.forwardUnknown = v
}

// Reconfigure implements config.Reconfigurable (partial; full Reset is done by proxy wrapper).
func (r *Router) Reconfigure(cfg config.Config) {
	r.SetForwardUnknown(cfg.ForwardUnknown)
}

var (
	errMidStream            = errors.New("stream aborted after first byte")
	contextDeadlineExceeded = deadlineErr{}
)

// StreamAborted wraps an error that occurred after the first stream byte was
// sent to the client. Failover must NOT retry these.
func StreamAborted() error { return errMidStream }

// IsStreamAborted reports whether err is a stream-abort sentinel.
func IsStreamAborted(err error) bool { return errors.Is(err, errMidStream) }

type deadlineErr struct{}

func (deadlineErr) Error() string { return "context deadline exceeded" }
func (deadlineErr) Is(target error) bool {
	return target.Error() == "context deadline exceeded"
}
