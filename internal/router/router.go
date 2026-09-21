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
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mariobgsp/routre/internal/config"
)

// Router is a concurrency-safe tiered provider list with cooldowns.
type Router struct {
	mu     sync.RWMutex
	discTS atomic.Int64     // last successful discovery unix seconds (0 = never)
	provs  []*ProviderState // flattened in tier order
	policy CooldownPolicy
	now    func() time.Time // clock injection for tests
	// forwardUnknown: when true, a model absent from every provider's
	// whitelist is still forwarded verbatim to all available providers
	// (tier order, failover). Enables zero-config handling of new/future
	// models. Set via SetForwardUnknown.
	forwardUnknown bool
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

// providerInstanceKey identifies a configured upstream instance. A reload that
// changes the endpoint (base_url) or the kind is a DIFFERENT upstream, so it
// must start with a fresh cooldown instead of inheriting the old one's.
func providerInstanceKey(name, kind, baseURL string) string {
	return name + "\x00" + kind + "\x00" + baseURL
}

// Reset reconciles the provider list and policy in place (config reload).
// Providers that still exist with the same endpoint keep their *ProviderState
// — and therefore their failures and cooldown — so a report from a request
// already in flight is not orphaned, and an unrelated config edit does not
// silently wipe every cooldown. A provider removed from the config, or the
// same name pointed at a different kind/base_url, loses its state so a repaired
// endpoint is not stuck in the old one's cooldown.
func (r *Router) Reset(tiers []TierInput, policy CooldownPolicy) {
	r.mu.Lock()
	defer r.mu.Unlock()
	existing := make(map[string]*ProviderState, len(r.provs))
	for _, p := range r.provs {
		existing[providerInstanceKey(p.Provider.Name, p.Provider.Kind, p.Provider.BaseURL)] = p
	}
	next := make([]*ProviderState, 0, len(r.provs))
	for ti, t := range tiers {
		for _, in := range t.Providers {
			key := providerInstanceKey(in.Name, in.Kind, in.BaseURL)
			st, ok := existing[key]
			if !ok {
				st = &ProviderState{}
			}
			// Update only the static fields; failures/until are preserved.
			st.Provider = ProviderInfo{
				Name:      in.Name,
				Kind:      in.Kind,
				BaseURL:   in.BaseURL,
				APIKeyEnv: in.APIKeyEnv,
				Models:    in.Models,
				Tier:      t.Name,
				TierIndex: ti,
				MaxTokens: in.MaxTokens,
			}
			next = append(next, st)
			// Consume the key so a duplicated provider later in the config gets
			// its own state rather than aliasing this one (deterministic: the
			// first occurrence reuses, later ones start fresh, in config order).
			delete(existing, key)
		}
	}
	r.provs = next
	r.policy = policy
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

var errMidStream = errors.New("stream aborted after first byte")

// StreamAborted wraps an error that occurred after the first stream byte was
// sent to the client. Failover must NOT retry these.
func StreamAborted() error { return errMidStream }

// IsStreamAborted reports whether err is a stream-abort sentinel.
func IsStreamAborted(err error) bool { return errors.Is(err, errMidStream) }
