// Package config defines the on-disk JSON configuration for routre-cli.
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
}

// CacheConfig mirrors cache.Config for JSON.
type CacheConfig struct {
	Enabled     bool  `json:"enabled"`
	MaxEntries  int   `json:"max_entries"`
	TTLSeconds  int64 `json:"ttl_seconds"`
	PrefixOrder bool  `json:"prefix_order"`
	MaxBytes    int64 `json:"max_bytes"`
}

// Config is the root document.
type Config struct {
	// Listen is the bind address, e.g. "127.0.0.1:20128".
	Listen   string      `json:"listen"`
	LogLevel string      `json:"log_level"`
	Tiers    []Tier      `json:"tiers"`
	RTK      RTKConfig   `json:"rtk"`
	Cache    CacheConfig `json:"cache"`
	// RequestLog: path of the per-request JSONL log ("" = disabled).
	RequestLog string `json:"request_log"`
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
}

// Default returns the built-in defaults (3-tier shape must come from the
// user config; defaults apply to optional fields).
func Default() Config {
	return Config{
		Listen:   "127.0.0.1:20128",
		LogLevel: "info",
		Tiers:    []Tier{},
		RTK:      RTKConfig{Enabled: true, MinBytes: 0, MaxBytes: 10 << 20},
		Cache:    CacheConfig{Enabled: true, MaxEntries: 4096, TTLSeconds: 86400, PrefixOrder: true, MaxBytes: 64 << 20},
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
			if p.Kind != KindOpenAI && p.Kind != KindAnthropic {
				return fmt.Errorf("config: provider %q kind %q must be %q or %q",
					p.Name, p.Kind, KindOpenAI, KindAnthropic)
			}
			if p.BaseURL == "" {
				return fmt.Errorf("config: provider %q has no base_url", p.Name)
			}
			if p.APIKeyEnv == "" {
				return fmt.Errorf("config: provider %q has no api_key_env", p.Name)
			}
			if len(p.Models) == 0 {
				return fmt.Errorf("config: provider %q has no models", p.Name)
			}
			if p.PriceIn < 0 || p.PriceOut < 0 {
				return fmt.Errorf("config: provider %q prices must be >= 0", p.Name)
			}
		}
	}
	if c.RTK.MinBytes < 0 || c.RTK.MaxBytes < 0 {
		return errors.New("config: rtk byte bounds must be >= 0")
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

// Store holds the live config with reload support. Safe for concurrent use.
type Store struct {
	mu     sync.RWMutex
	path   string
	cfg    Config
	onLoad func(Config) // called after every successful load/reload
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

// Load reads the config file. Missing file: defaults, no error. Invalid
// content: error, previous config retained. The sibling routre-cli.env key
// file (if present) is loaded into the process environment first, so users
// never need shell exports.
func (s *Store) Load() error {
	if err := LoadEnvFile(EnvFilePath(s.path)); err != nil {
		return err
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			// Defaults are already in place.
			return nil
		}
		return fmt.Errorf("config: read %s: %w", s.path, err)
	}
	cfg := Default()
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("config: parse %s: %w", s.path, err)
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	s.mu.Lock()
	s.cfg = cfg
	fn := s.onLoad
	s.mu.Unlock()
	if fn != nil {
		fn(cfg)
	}
	return nil
}

// Reload re-reads the config file, applying the OnLoad callback on success.
// The env file is reloaded first so newly added keys take effect without a
// restart.
func (s *Store) Reload() error {
	if err := LoadEnvFile(EnvFilePath(s.path)); err != nil {
		return err
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("config: reload read %s: %w", s.path, err)
	}
	cfg := Default()
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("config: reload parse %s: %w", s.path, err)
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("config: reload: %w", err)
	}
	s.mu.Lock()
	s.cfg = cfg
	fn := s.onLoad
	s.mu.Unlock()
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

// OverrideListen applies a CLI -port/-listen override on top of the loaded
// config without touching the file.
func (s *Store) OverrideListen(listen string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.Listen = listen
}
