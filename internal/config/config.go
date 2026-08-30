// Package config defines the on-disk JSON configuration for routre.
// It supports SIGHUP reload: Load once at startup, then Reload on SIGHUP,
// and every subsystem receives the new config through the OnLoad callback.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
)

// Kind identifies the upstream API dialect.
type Kind string

const (
	KindOpenAI    Kind = "openai"
	KindAnthropic Kind = "anthropic"
	KindGemini    Kind = "gemini"
)

// Provider is one upstream endpoint within a tier.
type Provider struct {
	Name      string   `json:"name"`
	Kind      Kind     `json:"kind"`
	BaseURL   string   `json:"base_url"`
	APIKeyEnv string   `json:"api_key_env"` // env var holding the API key
	Models    []string `json:"models"`
	// PriceIn / PriceOut: USD per 1M tokens, for cost reporting. Zero =
	// unknown (reported as n/a).
	PriceIn  float64 `json:"price_in,omitempty"`
	PriceOut float64 `json:"price_out,omitempty"`
	// MaxTokens: ceiling applied to the request's max_tokens when relaying
	// to this provider (0 = no clamp). Lets a fallback provider with a
	// smaller context window accept requests sized for the preferred one.
	MaxTokens int64 `json:"max_tokens,omitempty"`
}

// Tier is an ordered fallback group.
type Tier struct {
	Name      string     `json:"name"`
	Providers []Provider `json:"providers"`
}

// RTKConfig mirrors rtk.Config for JSON.
type RTKConfig struct {
	Enabled  bool `json:"enabled"`
	MinBytes int  `json:"min_bytes"`
	MaxBytes int  `json:"max_bytes"`
	// Level: "", "standard", or "routre" (routre level — ultra-aggressive).
	Level string `json:"level,omitempty"`
}

// CacheConfig mirrors cache.Config for JSON.
type CacheConfig struct {
	Enabled     bool  `json:"enabled"`
	MaxEntries  int   `json:"max_entries"`
	TTLSeconds  int64 `json:"ttl_seconds"`
	PrefixOrder bool  `json:"prefix_order"`
	MaxBytes    int64 `json:"max_bytes"`
	// CanonicalKeys: when true, the cache key is computed over a
	// deterministic JSON round-trip (sorted keys, no whitespace) so that
	// semantically identical requests differing only in byte layout share
	// a key. Strictly output-inert: sampling parameters are preserved.
	CanonicalKeys bool `json:"canonical_keys,omitempty"`
	// SlidingTTL: when true, a hit refreshes the entry's expiry
	// (now + TTL), so hot entries never expire while actively used.
	SlidingTTL bool `json:"sliding_ttl,omitempty"`
	// PromptCache: when true, the gateway injects Anthropic cache_control
	// breakpoints (system prefix + last message) into Anthropic-bound
	// outbound requests so repeat agentic prefixes are billed at the cache
	// read rate. Passthrough is byte-preserving: an already-present
	// cache_control is never rewritten or stripped. Off by default because
	// injection changes the request body.
	PromptCache bool `json:"prompt_cache,omitempty"`
}

// AuthConfig is the optional gateway shared-secret protection. When
// SecretEnv is set (and non-empty), every /v1/* request must present the
// matching secret. Off by default (SecretEnv empty) — zero-config behavior
// is unchanged.
type AuthConfig struct {
	// SecretEnv is the env var (from routre.env or the environment)
	// holding the shared secret. Empty = auth disabled.
	SecretEnv string `json:"secret_env,omitempty"`
	// Header is the HTTP header carrying the secret. Defaults to
	// X-Routre-Key when empty. "Authorization: Bearer <secret>" is also
	// accepted.
	Header string `json:"header,omitempty"`
}

// Config is the root document.
type Config struct {
	// Listen is the bind address, e.g. "127.0.0.1:20128".
	Listen   string      `json:"listen"`
	LogLevel string      `json:"log_level"`
	Tiers    []Tier      `json:"tiers"`
	RTK      RTKConfig   `json:"rtk"`
	Cache    CacheConfig `json:"cache"`
	// Auth: optional shared-secret protection for the gateway port.
	Auth AuthConfig `json:"auth"`
	// RequestLog: path of the per-request JSONL log ("" = disabled).
	RequestLog string `json:"request_log"`
	// HealthCheck: optional periodic per-provider probe so cooldown /
	// availability changes are visible between real requests. Disabled
	// by default (a noisy provider that flaps is worse than a silent
	// one). IntervalSeconds=0 disables. ProbeModel is the model used to
	// exercise each provider; "" = first listed model.
	HealthCheck HealthCheckConfig `json:"health_check"`
	// PreferredModel: the user's default model (their preferred provider).
	// Informational for routing — clients request it explicitly — but used
	// by `setup` to seed the config and by `check` to display the default.
	PreferredModel string `json:"preferred_model"`
	// Fallbacks: ordered list of models tried after the requested model's
	// own candidates fail. Any model the user has access to: free tiers,
	// paid models on other providers, or same-provider alternatives. Each
	// entry is routed through the normal candidate logic (exact + free
	// variants).
	Fallbacks []string `json:"fallbacks"`
	// ForwardUnknown: when true (default), a model not listed in any
	// provider's `models` whitelist is still forwarded verbatim to every
	// available provider in tier order, failing over automatically. New
	// and future models then work with no config edit — the upstream is
	// authoritative. When false, an unknown model returns model_not_found
	// (the original 402-cascade guard).
	ForwardUnknown bool `json:"forward_unknown"`
	// Budgets: optional per-client daily USD caps, e.g. {"codex": 5.0}.
	// When set, `routre list` surfaces BUDGET HIT and the gateway
	// skips providers whose ledger exceeds the cap (cheap fallback).
	// ponytail: warn-only in list; hard cooldown if hit rate proves useful.
	Budgets map[string]float64 `json:"budgets,omitempty"`
}

// HealthCheckConfig controls the optional per-provider periodic probe.
type HealthCheckConfig struct {
	Enabled         bool `json:"enabled"`
	IntervalSeconds int  `json:"interval_seconds"`
	// ProbeModel overrides the model used for probes. When empty, the
	// first listed model on each provider is used.
	ProbeModel string `json:"probe_model,omitempty"`
}

// Default returns the built-in defaults (3-tier shape must come from the
// user config; defaults apply to optional fields).
func Default() Config {
	return Config{
		Listen:   "127.0.0.1:20128",
		LogLevel: "info",
		Tiers:    []Tier{},
		RTK:      RTKConfig{Enabled: true, MinBytes: 0, MaxBytes: 10 << 20},
		Cache:    CacheConfig{Enabled: true, MaxEntries: 4096, TTLSeconds: 86400, PrefixOrder: true, MaxBytes: 64 << 20, CanonicalKeys: true, SlidingTTL: true},
		// Zero-config model handling: unknown/future models forward to all
		// providers and fail over, instead of requiring a whitelist edit.
		ForwardUnknown: true,
		// Periodic health probes: off by default. Turn on to surface
		// provider outages between real requests.
		HealthCheck: HealthCheckConfig{Enabled: false, IntervalSeconds: 30},
	}
}

// Validate checks semantic constraints. It does NOT require tiers to be
// non-empty (a config without tiers is valid but useless; the proxy will
// serve 503s).
func (c *Config) Validate() error {
	if c.Listen == "" {
		return errors.New("config: listen address is empty")
	}
	seen := map[string]bool{}
	for i, tier := range c.Tiers {
		if tier.Name == "" {
			return fmt.Errorf("config: tier %d has no name", i)
		}
		for j, p := range tier.Providers {
			if p.Name == "" {
				return fmt.Errorf("config: tier %q provider %d has no name", tier.Name, j)
			}
			if seen[p.Name] {
				return fmt.Errorf("config: duplicate provider name %q", p.Name)
			}
			seen[p.Name] = true
			if p.Kind != KindOpenAI && p.Kind != KindAnthropic && p.Kind != KindGemini {
				return fmt.Errorf("config: provider %q kind %q must be %q, %q, or %q",
					p.Name, p.Kind, KindOpenAI, KindAnthropic, KindGemini)
			}
			if p.BaseURL == "" {
				return fmt.Errorf("config: provider %q has no base_url", p.Name)
			}
			if p.APIKeyEnv == "" {
				return fmt.Errorf("config: provider %q has no api_key_env", p.Name)
			}
			// Models may be empty: the gateway auto-discovers the provider's
			// own /v1/models list at runtime (see router.DiscoverModels).
			// Empty is valid but useless until discovery succeeds.
			if p.PriceIn < 0 || p.PriceOut < 0 {
				return fmt.Errorf("config: provider %q prices must be >= 0", p.Name)
			}
		}
	}
	if c.RTK.MinBytes < 0 || c.RTK.MaxBytes < 0 {
		return errors.New("config: rtk byte bounds must be >= 0")
	}
	switch c.RTK.Level {
	case "", "standard", "routre", "caveman": // caveman kept as deprecated alias for routre
	default:
		return fmt.Errorf("config: rtk.level must be \"standard\" or \"routre\", got %q", c.RTK.Level)
	}
	if c.RTK.MinBytes > c.RTK.MaxBytes {
		return errors.New("config: rtk.min_bytes > rtk.max_bytes")
	}
	if c.Cache.MaxEntries < 0 {
		return errors.New("config: cache.max_entries must be >= 0")
	}
	if c.Cache.TTLSeconds < 0 {
		return errors.New("config: cache.ttl_seconds must be >= 0")
	}
	if c.Cache.MaxBytes < 0 {
		return errors.New("config: cache.max_bytes must be >= 0")
	}
	return nil
}

// Reconfigurable is a subsystem that can apply a new Config without restart.
type Reconfigurable interface{ Reconfigure(Config) }

// Store holds the live config with reload support. Safe for concurrent use.
type Store struct {
	mu              sync.RWMutex
	path            string
	cfg             Config
	onLoad          func(Config) // legacy single-callback (kept for compat)
	reconfigurables []Reconfigurable
}

// NewStore creates a store around the config file at path. If the file does
// not exist, defaults are used (warn is surfaced to the caller by checking
// Loaded()).
func NewStore(path string) *Store {
	return &Store{path: path, cfg: Default()}
}

// SetOnLoad registers the callback invoked after each successful
// load/reload with the new config. Replaces any previous callback.
func (s *Store) SetOnLoad(fn func(Config)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onLoad = fn
}

// Register adds a Reconfigurable to be called on every successful Load/Reload.
// Order of registration is the order of Reconfigure calls.
func (s *Store) Register(r Reconfigurable) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reconfigurables = append(s.reconfigurables, r)
}

// Load reads the config file. Missing file: defaults, no error. Invalid
// content: error, previous config retained. The sibling routre.env key
// file (if present) is loaded into the process environment first, so users
// never need shell exports.
func (s *Store) Load() error {
	return s.loadLocked("load")
}

// Reload re-reads the config file, applying the OnLoad callback on success.
// The env file is reloaded first so newly added keys take effect without a
// restart.
func (s *Store) Reload() error {
	return s.loadLocked("reload")
}

// loadLocked is the shared read+validate+swap used by Load and Reload.
// what is the verb used in error messages ("load" or "reload").
func (s *Store) loadLocked(what string) error {
	if err := LoadEnvFile(EnvFilePath(s.path)); err != nil {
		return err
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if what == "load" && os.IsNotExist(err) {
			// Defaults are already in place.
			return nil
		}
		return fmt.Errorf("config: %s read %s: %w", what, s.path, err)
	}
	cfg := Default()
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("config: %s parse %s: %w", what, s.path, err)
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("config: %s: %w", what, err)
	}
	s.mu.Lock()
	s.cfg = cfg
	fn := s.onLoad
	reconfigs := append([]Reconfigurable(nil), s.reconfigurables...)
	s.mu.Unlock()
	for _, r := range reconfigs {
		r.Reconfigure(cfg)
	}
	if fn != nil {
		fn(cfg)
	}
	return nil
}

// Get returns the current config.
func (s *Store) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// Path returns the config file path.
func (s *Store) Path() string { return s.path }

// Save validates cfg, writes it atomically to the store's file, and reloads.
// It triggers the OnLoad callback on success. The write is via temp file +
// rename, so readers never see a torn file.
func (s *Store) Save(cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := s.path
	if idx := len(s.path); idx > 0 {
		// filepath.Dir without extra import (path is already validated)
		for i := len(s.path) - 1; i >= 0; i-- {
			if s.path[i] == '/' || s.path[i] == '\\' {
				dir = s.path[:i]
				break
			}
		}
		if dir == s.path {
			dir = "."
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	return s.Load()
}

// OverrideListen applies a CLI -port/-listen override on top of the loaded
// config without touching the file.
func (s *Store) OverrideListen(listen string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.Listen = listen
}
