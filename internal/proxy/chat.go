package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"routre-cli/internal/cache"
	"routre-cli/internal/reqlog"
	"routre-cli/internal/router"
	"routre-cli/internal/tokenize"
	"routre-cli/internal/usage"
)

// attemptTimeout bounds a single non-streaming upstream attempt. Streaming
// relays are exempt (they can legitimately run for minutes; the transport
// already bounds dial + response headers).
const attemptTimeout = 120 * time.Second

// rewriteModel swaps the model field of a JSON request body, returning the
// new body. On malformed input it returns the original body unchanged.
func rewriteModel(body []byte, model string) ([]byte, error) {
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		return body, nil
	}
	doc["model"] = model
	out, err := json.Marshal(doc)
	if err != nil {
		return body, nil
	}
	return out, nil
}

// clampMaxTokens caps the max_tokens field of a JSON request body so that
// the total context (prompt + max_tokens) fits the provider's ceiling. The
// ceiling is the provider's TOTAL context window; upstreams reject
// requests where prompt tokens + max_tokens exceed it, so the clamp
// subtracts an estimate of the prompt size plus a small safety margin.
// Returns the original body when the ceiling is 0 (unset), the body has
// no max_tokens, or it is already within bounds.
func clampMaxTokens(body []byte, ceiling int64) ([]byte, error) {
	if ceiling <= 0 {
		return body, nil
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		return body, nil
	}
	mt, ok := doc["max_tokens"]
	if !ok {
		return body, nil
	}
	var n int64
	switch v := mt.(type) {
	case float64:
		n = int64(v)
	case json.Number:
		n, _ = v.Int64()
	default:
		return body, nil
	}
	// Estimate the prompt side (messages/system/tools) from the body with
	// the max_tokens field excluded so the estimate is not inflated by it.
	promptEst := int64(0)
	if msgs, ok := doc["messages"]; ok {
		if mb, err := json.Marshal(msgs); err == nil {
			promptEst = int64(tokenize.Estimate(string(mb)))
		}
	}
	// Reserve margin for tokenizer drift between our estimate and the
	// provider's exact count.
	const margin = 512
	maxAllowed := ceiling - promptEst - margin
	if maxAllowed < 1024 {
		maxAllowed = 1024
	}
	if n <= maxAllowed {
		return body, nil
	}
	doc["max_tokens"] = maxAllowed
	out, err := json.Marshal(doc)
	if err != nil {
		return body, nil
	}
	return out, nil
}

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
	start := time.Now()
	ctx := r.Context()
	client := clientName(r)

	// Request-log + metrics emission on every exit path.
	logReq := func(e reqlog.Entry) {
		e.LatencyMS = time.Since(start).Milliseconds()
		reqlog.Write(e)
	}

	body, err := readBody(r.Body, maxRequestBody)
	if err != nil {
		logReq(reqlog.Entry{Client: client, Status: http.StatusRequestEntityTooLarge, Class: "error"})
		h.Metrics.Request(client, "", "", "error")
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{
			"error": map[string]any{"message": "request body too large", "type": "invalid_request_error"},
		})
		return
	}
	if len(body) == 0 {
		logReq(reqlog.Entry{Client: client, Status: http.StatusBadRequest, Class: "error"})
		h.Metrics.Request(client, "", "", "error")
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]any{"message": "empty request body", "type": "invalid_request_error"},
		})
		return
	}

	streaming := isStreaming(body)
	requested := modelFromBody(body)

	// 1) RTK compression (token reduction).
	processed, rtkChanged := h.RTK.Apply(body)
	rtkSaved := 0
	if rtkChanged {
		rtkSaved = tokenize.Estimate(string(body)) - tokenize.Estimate(string(processed))
		h.Metrics.RTKApplied()
	}
	h.Metrics.RTKSaved(int64(rtkSaved))
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
			h.Metrics.CacheHit()
			logReq(reqlog.Entry{Client: client, Model: requested, Status: http.StatusOK, Class: "cache", PromptTokens: int64(cacheSaved), RTKSavedTokens: int64(rtkSaved)})
			w.Header().Set("Content-Type", e.ContentType)
			w.Header().Set("X-Llrouter-Cache", "hit")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(e.Body)
			return
		}
		h.Metrics.CacheMiss()
	}

	// 4) Tiered failover relay, model-aware: only providers that can serve
	// the requested model (exact or free variant) are candidates, in tier
	// order. If the requested model has no working candidate, the
	// configured fallback models are tried in order — the gateway degrades
	// to the user's fallback models (free tiers, or paid models on another
	// provider) instead of erroring. Providers already attempted for this
	// request are not retried.
	cands := h.Router.CandidatesWithFallbacks(requested, h.Cfg.Get().Fallbacks)
	if len(cands) == 0 {
		// Distinguish "model never configured" from "every provider that
		// could serve it is in cooldown" — both collapse to an empty
		// candidate list, but the remedies are different (fix the config
		// vs wait/check the provider).
		if retryAfter, served := h.Router.MinCooldownForModel(requested); served {
			h.Metrics.Request(client, "", requested, "providers_unavailable")
			logReq(reqlog.Entry{Client: client, Model: requested, Status: http.StatusServiceUnavailable, Class: "providers_unavailable"})
			if retryAfter < time.Second {
				retryAfter = time.Second
			}
			w.Header().Set("Retry-After", fmt.Sprintf("%d", int(retryAfter.Seconds())))
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"error": map[string]any{"message": fmt.Sprintf("all providers that serve model %q are cooling down (retry in %s)", requested, retryAfter.Round(time.Second)), "type": "providers_unavailable"},
			})
			return
		}
		h.Metrics.Request(client, "", requested, "model_not_found")
		logReq(reqlog.Entry{Client: client, Model: requested, Status: http.StatusServiceUnavailable, Class: "model_not_found"})
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": map[string]any{"message": fmt.Sprintf("no configured provider serves model %q (check config tiers/models)", requested), "type": "model_not_found"},
		})
		return
	}

	var lastErr error
	for _, cand := range cands {
		p := cand.Provider

		payload := processed
		kind := p.Provider.Kind
		// Model rewrite: send the candidate's upstream model when it differs
		// from what the client asked for. This covers free-variant routing
		// (deepseek-v4-flash -> deepseek-v4-flash-free) AND fallback-model
		// routing (deepseek-v4-flash -> openai/gpt-oss-20b:free when the
		// requested model's providers all failed).
		if cand.Upstream != requested {
			rewritten, rerr := rewriteModel(processed, cand.Upstream)
			if rerr == nil {
				payload = rewritten
			}
		}
		// Max-tokens clamp: cap the request's max_tokens to this provider's
		// ceiling when set. Without it a request sized for the preferred
		// model (subagents routinely ask for deepseek-v4-flash's full 384k
		// ceiling) is rejected by a fallback with a smaller context window
		// (e.g. OpenRouter free models, 131072) instead of failing over.
		if p.Provider.MaxTokens > 0 {
			clamped, cerr := clampMaxTokens(payload, p.Provider.MaxTokens)
			if cerr == nil {
				payload = clamped
			}
		}

		// Per-attempt timeout: a stuck provider must not hang the request
		// forever. Non-streaming relays get a hard deadline; streaming
		// relays keep the connection (they have their own first-byte
		// timeout in the transport).
		attemptCtx := ctx
		var cancel context.CancelFunc
		if !streaming {
			attemptCtx, cancel = context.WithTimeout(ctx, attemptTimeout)
		} else {
			attemptCtx, cancel = context.WithCancel(ctx)
		}

		if (api == fmtOpenAI && kind == "anthropic") || (api == fmtAnthropic && kind == "openai") {
			if streaming {
				cancel()
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
				cancel()
				h.Router.ReportFailure(p, router.ErrServer)
				lastErr = terr
				continue
			}
			payload = translated
		}

		status, respBody, ct, rerr := h.relay(attemptCtx, w, p.Provider.BaseURL, r, payload, streaming, kind, p.Provider.APIKeyEnv)
		cancel()

		if rerr != nil {
			// Network/transport failure: retryable (unless mid-stream).
			if router.IsStreamAborted(rerr) {
				// Client already received bytes; failover would duplicate.
				return
			}
			class := router.Classify(rerr)
			if !router.IsRetryableClass(class) {
				h.Metrics.Request(client, p.Provider.Name, requested, "error")
				logReq(reqlog.Entry{Client: client, Model: requested, Provider: p.Provider.Name, Stream: streaming, Status: http.StatusBadGateway, Class: "error"})
				writeJSON(w, http.StatusBadGateway, map[string]any{
					"error": map[string]any{"message": rerr.Error(), "type": "upstream_error"},
				})
				return
			}
			h.Metrics.Failure(p.Provider.Name, class.String())
			h.Router.ReportFailure(p, class)
			lastErr = rerr
			h.Logger.Printf("provider %s failed (%v): %v; failing over", p.Provider.Name, class, rerr)
			continue
		}

		if streaming && status >= 200 && status < 300 {
			// relay already streamed the response to the client. Report the
			// prompt side (streamed completion tokens are collected from the
			// SSE stream by streamRelay, which fills h.lastStreamUsage).
			// A non-2xx was returned by relayStream as (status, body, ...)
			// with nothing streamed yet, so it falls through to the shared
			// error handling below (classify, report failure, fail over).
			h.Router.ReportSuccess(p)
			prompt := tokenize.Estimate(string(processed))
			h.Usage.Record(client, modelFromBody(body), int64(prompt), 0, int64(rtkSaved), 0, pricesOf(h.Cfg.Get(), p.Provider.Name), 0)
			h.Metrics.Request(client, p.Provider.Name, requested, "ok")
			logReq(reqlog.Entry{Client: client, Model: requested, UpstreamModel: cand.Upstream, Provider: p.Provider.Name, Stream: true, Status: http.StatusOK, Class: "ok", PromptTokens: int64(prompt), RTKSavedTokens: int64(rtkSaved)})
			return
		}

		if !streaming && status >= 200 && status < 300 {
			h.Router.ReportSuccess(p)
			if ct == "" {
				ct = "application/json"
			}
			// Usage: parse provider-reported tokens when present; fall back
			// to estimates. RTK savings land on the provider's row.
			prompt, completion, reportedCost, cacheRead := usageFromBody(respBody, body)
			h.Usage.RecordFull(client, modelFromBody(body), prompt, completion, int64(rtkSaved), 0, cacheRead, pricesOf(h.Cfg.Get(), p.Provider.Name), reportedCost)
			h.Cache.Put(key, cacheEntry(respBody, ct))
			h.Metrics.Request(client, p.Provider.Name, requested, "ok")
			h.Metrics.CacheRead(cacheRead)
			logReq(reqlog.Entry{Client: client, Model: requested, UpstreamModel: cand.Upstream, Provider: p.Provider.Name, Stream: false, Status: status, Class: "ok", PromptTokens: prompt, CompletionTokens: completion, RTKSavedTokens: int64(rtkSaved), CacheReadTokens: cacheRead, CostUSD: reportedCost})
			w.Header().Set("Content-Type", ct)
			w.Header().Set("X-Llrouter-Cache", "miss")
			w.Header().Set("X-Llrouter-Provider", p.Provider.Name)
			if cand.IsFree {
				w.Header().Set("X-Llrouter-Free", cand.Upstream)
			}
			w.WriteHeader(status)
			_, _ = w.Write(respBody)
			return
		}

		// Upstream error status. Credits failures (402, or 401 with a
		// credits body) fail over WITHOUT cooldown escalation — the provider
		// may still serve free variants.
		class := router.ClassifyStatusBody(status, respBody)
		if !router.IsRetryableClass(class) {
			// Client-caused (400/404/422...): surface it, no failover.
			h.Metrics.Request(client, p.Provider.Name, requested, "client_error")
			logReq(reqlog.Entry{Client: client, Model: requested, Provider: p.Provider.Name, Stream: streaming, Status: status, Class: "client_error"})
			writeStatus(w, status, respBody, ct)
			return
		}
		h.Metrics.Failure(p.Provider.Name, class.String())
		h.Router.ReportFailure(p, class)
		lastErr = fmt.Errorf("provider %s: status %d (%v)", p.Provider.Name, status, class)
		h.Logger.Printf("provider %s status %d (%v); failing over", p.Provider.Name, status, class)
	}

	// All providers exhausted (or in cooldown).
	msg := "all providers unavailable"
	if lastErr != nil {
		msg = lastErr.Error()
	}
	h.Metrics.Request(client, "", requested, "all_failed")
	logReq(reqlog.Entry{Client: client, Model: requested, Status: http.StatusServiceUnavailable, Class: "all_failed"})
	w.Header().Set("Retry-After", "5")
	writeJSON(w, http.StatusServiceUnavailable, map[string]any{
		"error": map[string]any{"message": msg, "type": "all_providers_failed"},
	})
}

// isStreaming detects stream:true in the request body via JSON parsing.
// A literal `"stream":true` inside user content (a quoted string) is
// NOT treated as a streaming request; only the top-level field counts.
func isStreaming(body []byte) bool {
	var probe struct {
		Stream bool `json:"stream"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return false
	}
	return probe.Stream
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
	// Content-Type is required by some upstreams (opencode.ai returns 500
	// without it); the streaming path sets it too.
	req.Header.Set("Content-Type", "application/json")
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

	// Error bodies are capped well below the success-body limit: a
	// provider's giant error payload must not be buffered in full or
	// classified as a network failure.
	limit := int64(maxResponseRead)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		limit = maxUpstreamError
	}
	body, err := readBody(resp.Body, limit)
	if err != nil {
		return 0, nil, "", fmt.Errorf("read upstream response: %w", err)
	}
	return resp.StatusCode, body, resp.Header.Get("Content-Type"), nil
}

// relayStream streams an SSE response to the client, copying upstream
// headers. On success it returns (http.StatusOK, nil, "", nil) and the
// caller must not write anything further — the status reports that the
// stream was relayed, while upstream 4xx/5xx are returned as
// (status, body, ct, nil) with nothing written yet so the caller can
// classify, report the failure, and fail over. Errors before the first
// byte are retryable; errors after the first byte are
// router.StreamAborted().
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
	// Upstreams that sniff Accept for SSE negotiation should see an
	// explicit event-stream intent; default it when the client sent none.
	accept := r.Header.Get("Accept")
	if accept == "" {
		accept = "text/event-stream"
	}
	req.Header.Set("Accept", accept)
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
	// Beta-feature passthroughs (mirror the non-streaming relay; beta-gated
	// features must work identically on both paths).
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

	// A non-2xx response is NOT a stream: return it so the caller can
	// classify, report the failure, and fail over to the next candidate
	// instead of streaming an error body to the client (which used to
	// reset the provider's cooldown via ReportSuccess). Error bodies are
	// capped like the non-streaming path.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, rerr := readBody(resp.Body, maxUpstreamError)
		if rerr != nil {
			return 0, nil, "", rerr
		}
		return resp.StatusCode, body, resp.Header.Get("Content-Type"), nil
	}

	if err := h.streamRelay(w, resp); err != nil {
		return 0, nil, "", err
	}
	return http.StatusOK, nil, "", nil
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
