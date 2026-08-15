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
	"fmt"
	"sync"
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
	// ErrStream: failure after the first stream byte — the client already
	// received a partial response; retrying would duplicate output.
	ErrStream
)

var errClassNames = map[ErrClass]string{
	ErrNetwork:   "network",
	ErrTimeout:   "timeout",
	ErrRateLimit: "rate-limit",
	ErrAuth:      "auth",
	ErrServer:    "server",
	ErrClient:    "client",
	ErrStream:    "stream",
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
// exponential, 30min cap.
func DefaultCooldownPolicy() CooldownPolicy {
	return CooldownPolicy{Base: 2 * time.Second, Max: 30 * time.Minute, MaxHits: 30}
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

// IsRetryableClass reports whether a failure should escalate cooldown and
// trigger failover. Stream aborts and client-caused 4xx do not.
func IsRetryableClass(c ErrClass) bool {
	switch c {
	case ErrNetwork, ErrTimeout, ErrRateLimit, ErrAuth, ErrServer:
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
}

// Router is a concurrency-safe tiered provider list with cooldowns.
type Router struct {
	mu     sync.RWMutex
	provs  []*ProviderState // flattened in tier order
	policy CooldownPolicy
	now    func() time.Time // clock injection for tests
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
// Stream aborts (ErrStream) never escalate.
func (r *Router) ReportFailure(p *ProviderState, class ErrClass) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if class == ErrStream {
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
