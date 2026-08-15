package proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"routre-cli/internal/cache"
	"routre-cli/internal/router"
	"routre-cli/internal/tokenize"
	"routre-cli/internal/usage"
)

// apiFormat is the API dialect a request arrives in.
type apiFormat int

const (
	fmtUnknown apiFormat = iota
	fmtOpenAI
	fmtAnthropic
)

// detectFormat guesses the dialect from the request path and body shape.
func detectFormat(path string, body []byte) apiFormat {
	if strings.HasSuffix(path, "/messages") {
		return fmtAnthropic
	}
	if strings.HasSuffix(path, "/chat/completions") {
		return fmtOpenAI
	}
	// Fall back to body shape.
	if bytes.Contains(body, []byte(`"max_tokens"`)) && !bytes.Contains(body, []byte(`"stream_options"`)) {
		return fmtAnthropic
	}
	return fmtOpenAI
}

// route handles one chat-style request end to end: read, compress (RTK),
// order (cache), exact-match cache lookup, tiered failover relay, cache
// write. It is shared by the /v1/chat/completions and /v1/messages handlers.
func (h *Handlers) route(w http.ResponseWriter, r *http.Request, api apiFormat) {
	ctx := r.Context()
	client := clientName(r)

	body, err := readBody(r.Body, maxRequestBody)
	if err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{
			"error": map[string]any{"message": "request body too large", "type": "invalid_request_error"},
		})
		return
	}
	if len(body) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]any{"message": "empty request body", "type": "invalid_request_error"},
		})
		return
	}

	streaming := isStreaming(body)

	// 1) RTK compression (token reduction).
	processed, rtkChanged := h.RTK.Apply(body)
	rtkSaved := 0
	if rtkChanged {
		rtkSaved = tokenize.Estimate(string(body)) - tokenize.Estimate(string(processed))
	}
	// 2) Cache-friendly ordering (only when enabled; keeps stable prefix).
	if cfg := h.Cfg.Get(); cfg.Cache.PrefixOrder {
		processed = orderPrompt(processed)
	}

	// 3) Exact-match cache (non-streaming only).
	key := cacheKey(processed)
	if !streaming {
		if e, ok := h.Cache.Get(key); ok {
			// Cache hit: nothing reached any provider. Count the tokens we
			// just saved (prompt side) in the client's row.
			cacheSaved := tokenize.Estimate(string(processed))
			if cacheSaved > 0 {
				h.Usage.Record(client, modelFromBody(processed), 0, 0, 0, int64(cacheSaved), usage.Prices{}, 0)
			}
			w.Header().Set("Content-Type", e.ContentType)
			w.Header().Set("X-Llrouter-Cache", "hit")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(e.Body)
			return
		}
	}

	// 4) Tiered failover relay.
	var lastErr error
	idx := 0
	for {
		p := h.Router.Next(idx)
		if p == nil {
			break
		}
		idx++ // next iteration continues after this provider

		payload := processed
		kind := p.Provider.Kind
		if (api == fmtOpenAI && kind == "anthropic") || (api == fmtAnthropic && kind == "openai") {
			if streaming {
				writeJSON(w, http.StatusNotImplemented, map[string]any{
					"error": map[string]any{
						"message": "cross-kind streaming translation is not implemented; request a non-streaming call or point the client at a same-kind provider",
						"type":    "not_implemented",
					},
				})
				return
			}
			translated, terr := translateBody(api, kindOf(kind), processed)
			if terr != nil {
				h.Router.ReportFailure(p, router.ErrServer)
				lastErr = terr
				continue
			}
			payload = translated
		}

		status, respBody, ct, rerr := h.relay(ctx, w, p.Provider.BaseURL, r, payload, streaming, kind, p.Provider.APIKeyEnv)
		if rerr != nil {
			// Network/transport failure: retryable (unless mid-stream).
			if router.IsStreamAborted(rerr) {
				// Client already received bytes; failover would duplicate.
				return
			}
			class := router.Classify(rerr)
			if !router.IsRetryableClass(class) {
				writeJSON(w, http.StatusBadGateway, map[string]any{
					"error": map[string]any{"message": rerr.Error(), "type": "upstream_error"},
				})
				return
			}
			h.Router.ReportFailure(p, class)
			lastErr = rerr
			h.Logger.Printf("provider %s failed (%v): %v; failing over", p.Provider.Name, class, rerr)
			continue
		}

		if streaming {
			// relay already streamed the response to the client. Report the
			// prompt side (streamed completion tokens are collected from the
			// SSE stream by streamRelay, which fills h.lastStreamUsage).
			h.Router.ReportSuccess(p)
			prompt := tokenize.Estimate(string(processed))
			h.Usage.Record(client, modelFromBody(body), int64(prompt), 0, int64(rtkSaved), 0, pricesOf(h.Cfg.Get(), p.Provider.Name), 0)
			return
		}

		if status >= 200 && status < 300 {
			h.Router.ReportSuccess(p)
			if ct == "" {
				ct = "application/json"
			}
			// Usage: parse provider-reported tokens when present; fall back
			// to estimates. RTK savings land on the provider's row.
			prompt, completion, reportedCost := usageFromBody(respBody, body)
			h.Usage.Record(client, modelFromBody(body), prompt, completion, int64(rtkSaved), 0, pricesOf(h.Cfg.Get(), p.Provider.Name), reportedCost)
			h.Cache.Put(key, cacheEntry(respBody, ct))
			w.Header().Set("Content-Type", ct)
			w.Header().Set("X-Llrouter-Cache", "miss")
			w.Header().Set("X-Llrouter-Provider", p.Provider.Name)
			w.WriteHeader(status)
			_, _ = w.Write(respBody)
			return
		}

		// Upstream error status.
		class := router.ClassifyStatus(status)
		if !router.IsRetryableClass(class) {
			// Client-caused (400/404/422...): surface it, no failover.
			writeStatus(w, status, respBody, ct)
			return
		}
		h.Router.ReportFailure(p, class)
		lastErr = fmt.Errorf("provider %s: status %d", p.Provider.Name, status)
		h.Logger.Printf("provider %s status %d (%v); failing over", p.Provider.Name, status, class)
	}

	// All providers exhausted (or in cooldown).
	msg := "all providers unavailable"
	if lastErr != nil {
		msg = lastErr.Error()
	}
	w.Header().Set("Retry-After", "5")
	writeJSON(w, http.StatusServiceUnavailable, map[string]any{
		"error": map[string]any{"message": msg, "type": "all_providers_failed"},
	})
}

// isStreaming detects stream:true in the request body (both spacing
// variants). This is deliberately simple; exotic whitespace inside the
// literal is the documented limitation.
func isStreaming(body []byte) bool {
	return bytes.Contains(body, []byte(`"stream":true`)) || bytes.Contains(body, []byte(`"stream": true`))
}

// cacheKey is the exact-match key over the processed body (post-RTK,
// post-ordering).
func cacheKey(processed []byte) string {
	return cache.Key(processed)
}

// orderPrompt moves system messages to the front (stable prefix).
func orderPrompt(processed []byte) []byte {
	return cache.OrderPrompt(processed)
}

// cacheEntry wraps a response for the cache.
func cacheEntry(body []byte, ct string) cache.Entry {
	return cache.Entry{Body: body, ContentType: ct}
}

// kindOf maps a provider kind string to an apiFormat.
func kindOf(kind string) apiFormat {
	if kind == "anthropic" {
		return fmtAnthropic
	}
	return fmtOpenAI
}

// writeStatus copies an upstream error response to the client.
func writeStatus(w http.ResponseWriter, status int, body []byte, ct string) {
	if ct == "" {
		ct = "application/json"
	}
	w.Header().Set("Content-Type", ct)
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// relay performs the actual upstream call.
// For non-streaming it returns (status, body, contentType, err).
// For streaming it streams SSE to w and returns (0, nil, "", err) where err
// is nil on success, retryable on pre-first-byte failure, or
// router.StreamAborted() after the first byte.
func (h *Handlers) relay(ctx context.Context, w http.ResponseWriter, baseURL string, r *http.Request, payload []byte, streaming bool, kind, apiKeyEnv string) (int, []byte, string, error) {
	if streaming {
		return h.relayStream(ctx, w, baseURL, r, payload, kind, apiKeyEnv)
	}
	path := "/v1/chat/completions"
	if kind == "anthropic" {
		path = "/v1/messages"
	}
	base := strings.TrimRight(baseURL, "/")
	if strings.HasSuffix(base, "/v1") {
		// Base already includes the /v1 prefix (OpenAI-style base URLs).
		path = strings.TrimPrefix(path, "/v1")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, "", err
	}
	// Provider API key: the gateway holds the key (from api_key_env), NOT
	// the client. A client-sent Authorization header is only a placeholder
	// (many CLIs require one); it must never reach the upstream.
	providerKey, missing := upstreamKey(apiKeyEnv)
	if missing {
		return 0, nil, "", fmt.Errorf("provider key %s is not set (use `routre-cli setup` or export it)", apiKeyEnv)
	}
	if kind == "anthropic" {
		req.Header.Set("X-Api-Key", providerKey)
		req.Header.Set("Anthropic-Version", firstNonEmpty(r.Header.Get("Anthropic-Version"), "2023-06-01"))
	} else {
		req.Header.Set("Authorization", "Bearer "+providerKey)
	}
	// Provider-specific passthroughs.
	for _, hdr := range []string{"Anthropic-Version", "Anthropic-Beta", "OpenAI-Beta"} {
		if v := r.Header.Get(hdr); v != "" {
			req.Header.Set(hdr, v)
		}
	}

	resp, err := h.HTTPClient.Do(req)
	if err != nil {
		return 0, nil, "", err
	}
	defer resp.Body.Close()

	body, err := readBody(resp.Body, maxResponseRead)
	if err != nil {
		return 0, nil, "", fmt.Errorf("read upstream response: %w", err)
	}
	return resp.StatusCode, body, resp.Header.Get("Content-Type"), nil
}

// relayStream streams an SSE response to the client, copying upstream
// headers. On success it returns (0, nil, "", nil); the caller must not
// write anything further. Errors before the first byte are retryable;
// errors after the first byte are router.StreamAborted().
func (h *Handlers) relayStream(ctx context.Context, w http.ResponseWriter, baseURL string, r *http.Request, payload []byte, kind, apiKeyEnv string) (int, []byte, string, error) {
	path := "/v1/chat/completions"
	if kind == "anthropic" {
		path = "/v1/messages"
	}
	base := strings.TrimRight(baseURL, "/")
	if strings.HasSuffix(base, "/v1") {
		path = strings.TrimPrefix(path, "/v1")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", r.Header.Get("Accept"))
	// Same key policy as relay: the gateway's key wins, never the client's.
	providerKey, missing := upstreamKey(apiKeyEnv)
	if missing {
		return 0, nil, "", fmt.Errorf("provider key %s is not set (use `routre-cli setup` or export it)", apiKeyEnv)
	}
	if kind == "anthropic" {
		req.Header.Set("X-Api-Key", providerKey)
		req.Header.Set("Anthropic-Version", firstNonEmpty(r.Header.Get("Anthropic-Version"), "2023-06-01"))
	} else {
		req.Header.Set("Authorization", "Bearer "+providerKey)
	}

	resp, err := h.HTTPClient.Do(req)
	if err != nil {
		return 0, nil, "", err
	}
	defer resp.Body.Close()

	if err := h.streamRelay(w, resp); err != nil {
		return 0, nil, "", err
	}
	return 0, nil, "", nil
}

// streamRelay copies an SSE stream to the client. flushAfter counts bytes
// written so far; on first-byte success the error is wrapped as a stream
// abort (failover must not retry).
func (h *Handlers) streamRelay(w http.ResponseWriter, resp *http.Response) error {
	// Copy headers from upstream.
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set("X-Llrouter-Streaming", "true")
	w.WriteHeader(resp.StatusCode)

	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	written := int64(0)
	firstByte := true
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				// Client went away mid-stream: not an upstream failure.
				return nil
			}
			written += int64(n)
			if firstByte {
				firstByte = false
			}
			if flusher != nil && written >= flushInterval {
				flusher.Flush()
				written = 0
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				return nil
			}
			if firstByte {
				// Failed before any byte reached the client: retryable.
				return fmt.Errorf("upstream stream failed before first byte: %w", rerr)
			}
			return router.StreamAborted()
		}
	}
}

// upstreamKey returns the provider's API key from the environment. The
// gateway holds keys; clients never need to know them.
func upstreamKey(envName string) (string, bool) {
	v := os.Getenv(envName)
	if v == "" {
		return "", true
	}
	return v, false
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
