package proxy

import (
	"net/http"
	"strings"
)

// authEnabled reports whether gateway auth is configured (a secret_env is
// set). Off by default — zero-config behavior is byte-identical.
func (h *Handlers) authEnabled() bool {
	return h.Cfg.Get().Auth.SecretEnv != ""
}

// authHeaderName returns the configured header (default X-Routre-Key).
func (h *Handlers) authHeaderName() string {
	n := h.Cfg.Get().Auth.Header
	if n == "" {
		return "X-Routre-Key"
	}
	return n
}

// authorized reports whether the request carries the gateway secret (via the
// configured header or Authorization: Bearer) or the per-process CLI token.
func (h *Handlers) authorized(r *http.Request, processToken string) bool {
	cfg := h.Cfg.Get()
	if cfg.Auth.SecretEnv == "" {
		return true
	}
	secret, ok := h.Keys.Get(cfg.Auth.SecretEnv)
	if !ok || secret == "" {
		return false // auth misconfigured: no secret available
	}
	presented := r.Header.Get(h.authHeaderName())
	if presented == "" {
		presented = bearerKey(r)
	}
	if presented == "" {
		return false
	}
	// constant-time compare to avoid leaking timing on the secret.
	return secureEqual(presented, secret) || (processToken != "" && secureEqual(presented, processToken))
}

// secureEqual is a constant-time string comparison (length-independent
// enough for a shared-secret check).
func secureEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

// authMiddleware enforces the shared secret on /v1/* when auth is enabled.
// /healthz (probes) and /metrics (scrapers) are exempt; the CLI uses the
// per-process token for its own calls.
func (h *Handlers) authMiddleware(processToken string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !h.authEnabled() {
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/healthz" || r.URL.Path == "/metrics" || r.URL.Path == "/ui" || strings.HasPrefix(r.URL.Path, "/ui/") {
			next.ServeHTTP(w, r)
			return
		}
		if h.authorized(r, processToken) {
			next.ServeHTTP(w, r)
			return
		}
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"error": map[string]any{
				"message": "invalid or missing gateway key",
				"type":    "invalid_api_key",
			},
		})
	})
}
