// Package config defines the on-disk JSON configuration for routre.
// It supports SIGHUP reload: Load once at startup, then Reload on SIGHUP,
// and every subsystem receives the new config through the OnLoad callback.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"
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
	// ScanBytes bounds the regex scoring window: filters score at most this
	// many bytes of each tool result, so a 10 MiB blob cannot put
	// unbounded regex work on the request path. 0 = default (512 KiB).
	ScanBytes int `json:"scan_bytes,omitempty"`
	// BudgetMS is the wall-clock budget for compressing one request body.
	// 0 = default (150 ms). When exceeded, RTK keeps what it already
	// compressed and returns (never-grow contract preserved).
	BudgetMS int `json:"budget_ms,omitempty"`
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

// TimeoutsConfig bounds how long the gateway waits for an upstream before
// failing over. Zero values on load mean "default"; the defaults are chosen so
// a slow-but-healthy provider is given a real chance (45 s slice, 45 s
// one-time extension, 120 s header ceiling) while a stuck socket still fails
// over in bounded time.
type TimeoutsConfig struct {
	// CandidateSeconds: the soft slice one candidate gets for headers + first
	// body byte before failover moves on.
	CandidateSeconds int `json:"candidate_seconds,omitempty"`
	// RequestSeconds: the whole-request budget across all candidates.
	RequestSeconds int `json:"request_seconds,omitempty"`
	// HeaderSeconds: the hard ceiling for the header wait when the connection
	// is alive and the request body was accepted (the adaptive extension).
	HeaderSeconds int `json:"header_seconds,omitempty"`
	// ExtensionSeconds: how long one in-flight attempt is extended after its
	// soft slice expires while the socket is alive. 0 disables the extension.
	ExtensionSeconds int `json:"extension_seconds,omitempty"`
	// GenerationSeconds: the backstop for a single attempt after its first
	// byte; a legitimate long generation may run this long.
	GenerationSeconds int `json:"generation_seconds,omitempty"`
}

// Candidate, Request, Header, Extension and Generation return the configured
// durations.
func (t TimeoutsConfig) Candidate() time.Duration {
	return time.Duration(t.CandidateSeconds) * time.Second
}
func (t TimeoutsConfig) Request() time.Duration { return time.Duration(t.RequestSeconds) * time.Second }
func (t TimeoutsConfig) Header() time.Duration  { return time.Duration(t.HeaderSeconds) * time.Second }
func (t TimeoutsConfig) Extension() time.Duration {
	return time.Duration(t.ExtensionSeconds) * time.Second
}
func (t TimeoutsConfig) Generation() time.Duration {
	return time.Duration(t.GenerationSeconds) * time.Second
}

// PersistConfig controls the on-disk snapshot of the response cache and the
// lifetime metric counters, so a restart starts warm instead of cold.
type PersistConfig struct {
	Enabled bool `json:"enabled"`
	// Dir overrides the data directory. Empty = ROUTRE_CLI_DATA_DIR or
	// ~/.routre.
	Dir string `json:"dir,omitempty"`
	// IntervalSeconds is the autosave period. 0 = default (60).
	IntervalSeconds int `json:"interval_seconds,omitempty"`
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
	// Timeouts bounds upstream waits (see TimeoutsConfig).
	Timeouts TimeoutsConfig `json:"timeouts"`
	// Persist keeps the cache (and metric counters) warm across restarts.
	Persist PersistConfig `json:"persist"`
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
		RTK:      RTKConfig{Enabled: true, MinBytes: 0, MaxBytes: 10 << 20, Level: "routre", ScanBytes: 512 << 10, BudgetMS: 150},
		// Longer exact-match retention for pi agentic prefix reuse;
		// RAM still capped by MaxBytes LRU + SlidingTTL.
		Cache: CacheConfig{Enabled: true, MaxEntries: 16384, TTLSeconds: 604800, PrefixOrder: true, MaxBytes: 128 << 20, CanonicalKeys: true, SlidingTTL: true},
		// Zero-config model handling: unknown/future models forward to all
		// providers and fail over, instead of requiring a whitelist edit.
		ForwardUnknown: true,
		// Periodic health probes: off by default. Turn on to surface
		// provider outages between real requests.
		HealthCheck: HealthCheckConfig{Enabled: false, IntervalSeconds: 30},
		// Adaptive budgets: a slow-but-alive provider gets 45 s, one 45 s
		// extension while the socket is alive, and a 120 s header ceiling;
		// a genuine generation may run 5 minutes after its first byte.
		Timeouts: TimeoutsConfig{CandidateSeconds: 45, RequestSeconds: 180, HeaderSeconds: 120, ExtensionSeconds: 45, GenerationSeconds: 300},
		// Cache + counters survive a restart by default.
		Persist: PersistConfig{Enabled: true, IntervalSeconds: 60},
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
	// 0 means "use the default bounded-work budget", not "unbounded".
	if c.RTK.ScanBytes == 0 {
		c.RTK.ScanBytes = 512 << 10
	}
	if c.RTK.BudgetMS == 0 {
		c.RTK.BudgetMS = 150
	}
	if c.RTK.ScanBytes < 0 {
		return errors.New("config: rtk.scan_bytes must be >= 0")
	}
	if c.RTK.BudgetMS < 0 {
		return errors.New("config: rtk.budget_ms must be >= 0")
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

	// Timeouts: normalize the zero value to the documented defaults, then
	// reject combinations that would make the failover contract incoherent.
	t := &c.Timeouts
	if t.CandidateSeconds == 0 {
		t.CandidateSeconds = 45
	}
	if t.RequestSeconds == 0 {
		t.RequestSeconds = 180
	}
	if t.HeaderSeconds == 0 {
		t.HeaderSeconds = 120
	}
	if t.GenerationSeconds == 0 {
		t.GenerationSeconds = 300
	}
	if t.CandidateSeconds < 0 || t.RequestSeconds < 0 || t.HeaderSeconds < 0 ||
		t.ExtensionSeconds < 0 || t.GenerationSeconds < 0 {
		return errors.New("config: timeouts values must be >= 0")
	}
	if t.RequestSeconds < t.CandidateSeconds {
		return errors.New("config: timeouts.request_seconds must be >= timeouts.candidate_seconds")
	}
	if t.HeaderSeconds < t.CandidateSeconds {
		return errors.New("config: timeouts.header_seconds must be >= timeouts.candidate_seconds")
	}

	if c.Persist.IntervalSeconds == 0 {
		c.Persist.IntervalSeconds = 60
	}
	if c.Persist.IntervalSeconds < 0 {
		return errors.New("config: persist.interval_seconds must be >= 0")
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

// MergeModelIDs returns existing followed by every ID in discovered that is not
// already present: existing order is kept, new IDs are appended sorted. It never
// removes or reorders an existing ID — discovery is purely additive, so a
// provider that stops advertising a model never loses it from config.
// added reports the appended IDs, sorted.
func MergeModelIDs(existing, discovered []string) (merged, added []string) {
	seen := make(map[string]bool, len(existing)+len(discovered))
	merged = append([]string(nil), existing...)
	for _, m := range merged {
		seen[m] = true
	}
	for _, m := range discovered {
		if seen[m] {
			continue
		}
		seen[m] = true
		added = append(added, m)
	}
	sort.Strings(added)
	return append(merged, added...), added
}
