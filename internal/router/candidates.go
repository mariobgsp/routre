package router

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/mariobgsp/routre/internal/tokenize"
)

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
			promptEst = tokenize.ClampCount(string(mb))
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

// normalizeModelAlias accepts the short Muse names commonly used in older
// pi/OpenCode configurations. OpenCode's current IDs include the "spark"
// segment; keep the alias at the routing boundary so the upstream always
// receives its canonical model ID.
var modelAliases = map[string]string{
	"muse-1.2-contributor-free": "muse-spark-1.2-contributor-free",
	"muse-1.3-contributor-free": "muse-spark-1.3-contributor-free",
}

func normalizeModelAlias(model string) string {
	if canonical, ok := modelAliases[model]; ok {
		return canonical
	}
	if i := strings.Index(model, "/"); i > 0 {
		if canonical, ok := modelAliases[model[i+1:]]; ok {
			return model[:i+1] + canonical
		}
	}
	return model
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
	model = normalizeModelAlias(model)
	now := r.now()
	// Detect provider-qualified model (e.g. "commandcode/deepseek/...").
	// If qualified, only that provider should be considered — prevents
	// free-variant tail matching from hijacking the request to other providers.
	qualifiedFor := ""
	for _, q := range r.provs {
		if stripProviderPrefix(q.Provider.Name, model) != "" {
			qualifiedFor = q.Provider.Name
			break
		}
	}
	var out []Candidate
	for _, p := range r.provs {
		if now.Before(p.until) {
			continue
		}
		if qualifiedFor != "" && p.Provider.Name != qualifiedFor {
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

// ServesModel reports whether any provider (regardless of cooldown)
// could serve model. Qualified "<provider>/<model>" is always considered
// served when the provider exists (gateway is a forwarder; upstream is
// authoritative). Bare names still require a whitelist hit or free variant.
func (r *Router) ServesModel(model string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	model = normalizeModelAlias(model)
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
	model = normalizeModelAlias(model)
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
