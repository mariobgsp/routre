package proxy

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mariobgsp/routre/internal/config"
)

// upstreamKey returns the provider's API key from the environment. The
// gateway holds keys; clients never need to know them. It is the fallback
// for tests that construct a Handlers without a keystore.
func upstreamKey(envName string) (string, bool) {
	v := os.Getenv(envName)
	if v == "" {
		return "", true
	}
	return v, false
}

// providerKey returns the provider key from the gateway's keystore, falling
// back to the process environment when no keystore is wired (tests).
func (h *Handlers) providerKey(envName string) (string, bool) {
	if h != nil && h.Keys != nil {
		if v, ok := h.Keys.Get(envName); ok && v != "" {
			return v, false
		}
	}
	return upstreamKey(envName)
}

// opencode session id: stable per-gateway instance fallback for the
// x-opencode-session header opencode.ai/zen requires. Forwarded when the
// client sent one; injected only for native Responses upstreams.
// ponytail: stable per-instance, not per-request random. Upgrade to a
// per-client map if opencode optimizes on it.
var (
	opencodeSessID   string
	opencodeSessOnce sync.Once
)

func opencodeSessionID() string {
	opencodeSessOnce.Do(func() {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err == nil {
			opencodeSessID = hex.EncodeToString(b)
		} else {
			opencodeSessID = "routre-fallback-session"
		}
	})
	return opencodeSessID
}

// bearerKey extracts the bearer token from the incoming Authorization header
// (used as X-Api-Key for Anthropic-style upstreams).
func bearerKey(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// parseRetryAfter parses an upstream Retry-After header delay. It accepts
// both forms HTTP allows: an integer number of seconds, or an HTTP-date.
// Unparseable or negative values yield 0 (no delay).
func parseRetryAfter(s string) time.Duration {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if secs, err := strconv.ParseInt(s, 10, 64); err == nil {
		if secs < 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	// HTTP-date form: delay = date - now. Clamp negative to 0.
	if t, err := http.ParseTime(s); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// refreshCredentials re-reads the routre.env key file and reports
// whether the provider's API key actually changed as a result. The keystore
// serializes concurrent refreshes under its own mutex and never mutates the
// process environment.
func (h *Handlers) refreshCredentials(apiKeyEnv string) bool {
	_, changed := h.Keys.Refresh(config.EnvFilePath(h.Cfg.Path()), apiKeyEnv)
	return changed
}
