package proxy

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"routre-cli/internal/cache"
	"routre-cli/internal/config"
	"routre-cli/internal/keystore"
	"routre-cli/internal/metrics"
	"routre-cli/internal/reqlog"
	"routre-cli/internal/router"
	"routre-cli/internal/rtk"
	"routre-cli/internal/usage"
)

// Handlers bundles the gateway's dependencies. One instance, shared by the
// http.Server.
type Handlers struct {
	Cfg        *config.Store
	Router     *router.Router
	Cache      *cache.Cache
	RTK        *rtk.RTK
	Logger     *log.Logger
	Start      time.Time
	HTTPClient *http.Client
	Usage      *usage.Store
	Metrics    *metrics.Metrics
	// Keys holds provider API keys in memory (and, when auth is enabled, the
	// gateway's shared secret). Refresh swaps rotated keys atomically without
	// mutating the process environment.
	Keys *keystore.Store
}

// newHTTPClient builds the upstream transport. Note: no overall timeout —
// streaming responses can run arbitrarily long; per-phase timeouts bound
// connection setup and headers.
func newHTTPClient() *http.Client {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
	}
	return &http.Client{Transport: transport}
}

// NewHandlers wires the pieces. On config reload the mutable subsystems
// (router, cache, rtk) are updated from the new config.
func NewHandlers(st *config.Store, rtr *router.Router, cch *cache.Cache, tk *rtk.RTK, logger *log.Logger, use *usage.Store) *Handlers {
	h := &Handlers{
		Cfg:        st,
		Router:     rtr,
		Cache:      cch,
		RTK:        tk,
		Logger:     logger,
		Start:      time.Now(),
		HTTPClient: newHTTPClient(),
		Usage:      use,
		Metrics:    metrics.New(),
		Keys:       keystore.New(),
	}
	// Seed the keystore from the process environment (which Load populated
	// from routre-cli.env + shell exports). The keystore is the gateway's
	// source of truth for upstream keys thereafter.
	for _, t := range st.Get().Tiers {
		for _, p := range t.Providers {
			if v, ok := os.LookupEnv(p.APIKeyEnv); ok {
				h.Keys.Set(p.APIKeyEnv, v)
			}
		}
	}
	st.SetOnLoad(func(c config.Config) {
		// Rebuild router (provider lists may have changed). Cooldowns reset.
		rtr.Reset(tiersFromConfig(c), rtrPolicy(rtr))
		// Reset preserves forwardUnknown; re-apply so config EDITS to it take
		// effect on reload without a restart.
		rtr.SetForwardUnknown(c.ForwardUnknown)
		cch.Update(cache.Config{
			Enabled:     c.Cache.Enabled,
			MaxEntries:  c.Cache.MaxEntries,
			TTLSeconds:  c.Cache.TTLSeconds,
			PrefixOrder: c.Cache.PrefixOrder,
		})
		tk.Update(rtk.Config{
			Enabled:  c.RTK.Enabled,
			MinBytes: c.RTK.MinBytes,
			MaxBytes: c.RTK.MaxBytes,
		})
		reqlog.SetPath(c.RequestLog)
		logger.Printf("config reloaded: %d tiers, %d providers", len(c.Tiers), rtr.Len())
	})
	return h
}

// MetricsHandler renders Prometheus exposition text for the gateway.
func (h *Handlers) MetricsHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	h.Metrics.WriteProm(w)
}

// Health is the liveness probe (systemd / monitoring).
func (h *Handlers) Health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

// NotFound answers every unmatched path.
func (h *Handlers) NotFound(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusNotFound, map[string]any{
		"error": map[string]any{"message": "not found: only /v1/chat/completions, /v1/responses, /v1/messages, /v1/models, /v1/status, /healthz", "type": "not_found"},
	})
}

// Models lists every configured model as provider/model (OpenAI-compatible format).
func (h *Handlers) Models(w http.ResponseWriter, _ *http.Request) {
	type modelObj struct {
		ID string `json:"id"`
	}
	var data []modelObj
	for _, s := range h.Router.Status() {
		for _, m := range s.Models {
			data = append(data, modelObj{ID: s.Provider + "/" + m})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   data,
	})
}

// Status exposes provider health/cooldown state for the CLI and dashboards.
func (h *Handlers) Status(w http.ResponseWriter, _ *http.Request) {
	type prov struct {
		Name              string   `json:"name"`
		Tier              string   `json:"tier"`
		Kind              string   `json:"kind"`
		Models            []string `json:"models"`
		Failures          int      `json:"failures"`
		CooldownRemaining string   `json:"cooldown_remaining"`
	}
	provs := make([]prov, 0)
	for _, s := range h.Router.Status() {
		provs = append(provs, prov{
			Name:              s.Provider,
			Tier:              s.Tier,
			Kind:              s.Kind,
			Models:            s.Models,
			Failures:          s.Failures,
			CooldownRemaining: s.CooldownRemaining.Round(time.Second).String(),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"uptime_seconds":  int(time.Since(h.Start).Seconds()),
		"cache_entries":   h.Cache.Len(),
		"cache_bytes":     h.Cache.SizeBytes(),
		"rtk_enabled":     h.RTK.Enabled(),
		"providers":       provs,
		"cache_hit_ratio": h.Metrics.CacheHitRatio(),
		"rtk_applied":     h.Metrics.RTKAppliedCount(),
	})
}

// UsageReport exposes the token/cost accumulator for `routre-cli list`.
func (h *Handlers) UsageReport(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"rows": h.Usage.Snapshot()})
}

// writeJSON is a small helper with content-type + status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
