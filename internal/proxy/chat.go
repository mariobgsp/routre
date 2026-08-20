package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"routre-cli/internal/cache"
	"routre-cli/internal/config"
	"routre-cli/internal/reqlog"
	"routre-cli/internal/router"
	"routre-cli/internal/tokenize"
	"routre-cli/internal/usage"
)

// attemptTimeout bounds a single non-streaming upstream attempt. Streaming
// relays are exempt (they can legitimately run for minutes; the transport
// already bounds dial + response headers).
const attemptTimeout = 120 * time.Second

// retryTransientAttempts: how many times a candidate is retried on a
// transient failure (network error or 5xx) before failover moves on.
// Upstream 503 blips are common (opencode.ai had an hour-long one in
// production); a single fast retry absorbs them without escalating the
// provider's cooldown and burning every fallback in the same window.
const retryTransientAttempts = 1

// transientRetryDelay: pause between retries of the same candidate.
const transientRetryDelay = 500 * time.Millisecond

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

// clampMaxTokens caps the max_tokens field of an already-decoded doc in
// place so that the total context (prompt + max_tokens) fits the provider's
// ceiling. The ceiling is the provider's TOTAL context window; upstreams
// reject requests where prompt tokens + max_tokens exceed it, so the clamp
// subtracts an estimate of the prompt size plus a small safety margin.
// No-op when the ceiling is 0 (unset), the doc has no max_tokens, or it is
// already within bounds.
//
// ponytail: operates on the shared decoded doc (set by buildPayload) instead
// of re-decoding+marshaling the body, which cut two full JSON passes off the
// same-kind relay hot path. No numeric-fidelity or boundary change: the
// prompt estimate falls back to a length-based token  estimate exactly as
// the old byte-level clamp did.
func clampMaxTokens(doc map[string]any, ceiling int64) {
	if ceiling <= 0 {
		return
	}
	mt, ok := doc["max_tokens"]
	if !ok {
		return
	}
	var n int64
	switch v := mt.(type) {
	case float64:
		n = int64(v)
	case json.Number:
		n, _ = v.Int64()
	default:
		return
	}
	// Estimate the prompt side (messages/system/tools) from the doc with
	// the max_tokens field excluded so the estimate is not inflated by it.
	promptEst := int64(0)
	if msgs, ok := doc["messages"]; ok {
		if mb, err := json.Marshal(msgs); err == nil {
			promptEst = int64(tokenize.Count(string(mb), tokenize.KindOpenAI))
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
		return
	}
	doc["max_tokens"] = maxAllowed
}

// buildPayload materializes the upstream request bytes for a same-kind
// candidate from a single decoded doc, applying the model rewrite and the
// max_tokens clamp as field mutations before ONE marshal. This collapses
// the two per-mutation decode+marshal passes the relay used to run.
// Fail-open: malformed input (or marshal failure) passes the processed body
// through unchanged, matching the old per-step behavior.
func buildPayload(processed []byte, requested, upstream string, ceiling int64) []byte {
	var doc map[string]any
	if err := json.Unmarshal(processed, &doc); err != nil {
		return processed // malformed: pass through (fail-open)
	}
	if upstream != requested {
		doc["model"] = upstream
	}
	clampMaxTokens(doc, ceiling)
	out, err := json.Marshal(doc)
	if err != nil {
		return processed
	}
	return out
}

// apiFormat is the API dialect a request arrives in.
type apiFormat int

const (
	fmtUnknown apiFormat = iota
	fmtOpenAI
	fmtAnthropic
	fmtResponses
	fmtGemini
)

// detectFormat guesses the dialect from the request path and body shape.
func detectFormat(path string, body []byte) apiFormat {
	if strings.HasSuffix(path, "/messages") {
		return fmtAnthropic
	}
	if strings.HasSuffix(path, "/responses") {
		return fmtResponses
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

	// The dialect the CLIENT speaks (response boundary), which may differ
	// from the internal `api` used for upstream relay. For Responses API
	// requests we translate the inbound body to chat up front and keep the
	// upstream `api` as fmtOpenAI; the response is re-wrapped on exit.
	clientFmt := api

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

	// Responses API: translate the inbound request to chat.completions so
	// the rest of the pipeline (RTK, ordering, cache, candidate relay) is
	// unchanged. Response re-wrapping happens on exit per clientFmt.
	if api == fmtResponses {
		translated, terr := responsesToOpenAI(body)
		if terr != nil {
			logReq(reqlog.Entry{Client: client, Status: http.StatusBadRequest, Class: "error"})
			h.Metrics.Request(client, "", "", "error")
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": map[string]any{"message": "could not parse Responses request: " + terr.Error(), "type": "invalid_request_error"},
			})
			return
		}
		body = translated
		api = fmtOpenAI
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
			// just saved (prompt side) in the client's row. Prefer the
			// upstream-reported count stored on the entry over the
			// gateway's length-based estimate (the estimate drifts on large
			// payloads: e.g. 160k vs the provider's 191k).
			cacheSaved := e.PromptTokens
			if cacheSaved == 0 {
				cacheSaved = int64(tokenize.Count(string(processed), tokenize.KindOpenAI))
			}
			if cacheSaved > 0 {
				h.Usage.Record(client, modelFromBody(processed), 0, 0, 0, cacheSaved, usage.Prices{}, 0)
			}
			h.Metrics.CacheHit()
			logReq(reqlog.Entry{Client: client, Model: requested, Status: http.StatusOK, Class: "cache", PromptTokens: cacheSaved, CompletionTokens: e.CompletionTokens, RTKSavedTokens: int64(rtkSaved)})
			// Responses API client: the cache stores the chat envelope, so
			// re-wrap before serving.
			cacheBody := e.Body
			cacheCT := e.ContentType
			if clientFmt == fmtResponses {
				if wrapped, werr := openAIToResponses(e.Body, requested); werr == nil {
					cacheBody = wrapped
					cacheCT = "application/json"
				}
			}
			w.Header().Set("Content-Type", cacheCT)
			w.Header().Set("X-Llrouter-Cache", "hit")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(cacheBody)
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
		// Retry transient failures (network errors, 5xx) on the same
		// candidate before failing over: upstream 503 blips resolve in
		// seconds, and each retry costs only ~0.5s. Cooldown escalation
		// only happens after the retries are exhausted.
		attempts := 1 + retryTransientAttempts
		for attempt := 0; attempt < attempts; attempt++ {
			if attempt > 0 {
				time.Sleep(transientRetryDelay)
			}
			ok := h.tryCandidate(ctx, w, r, api, cand, requested, body, processed, streaming, client, rtkSaved, clientFmt, &lastErr, logReq)
			if ok {
				return
			}
		}
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

// tryCandidate relays the request to one candidate provider and reports
// whether the response was fully handled. It returns true when the client
// got a final response (success, surfaced client error, mid-stream abort)
// and false when the failure is retryable so the caller can retry the same
// candidate or fail over to the next one.
//
// The caller owns the transient-retry loop around this: a provider that
// fails once is retried (transientRetryAttempts) before its failure is
// reported to the router's cooldown state.
func (h *Handlers) tryCandidate(ctx context.Context, w http.ResponseWriter, r *http.Request, api apiFormat, cand router.Candidate, requested string, body []byte, processed []byte, streaming bool, client string, rtkSaved int, clientFmt apiFormat, lastErr *error, logReq func(reqlog.Entry)) bool {
	p := cand.Provider

	kind := p.Provider.Kind
	// Same-kind payload: when a mutation is needed (free-variant / fallback
	// model rewrite, or a max_tokens ceiling) do one decode -> field
	// mutations -> one marshal (the old path did a decode+marshal per
	// mutation). When neither applies, pass the processed body through with
	// zero re-encoding.
	payload := processed
	if cand.Upstream != requested || p.Provider.MaxTokens > 0 {
		payload = buildPayload(processed, requested, cand.Upstream, p.Provider.MaxTokens)
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
	defer cancel()

	crossKind := (api == fmtOpenAI && kind == "anthropic") || (api == fmtAnthropic && kind == "openai") || (api == fmtOpenAI && kind == "gemini")
	// Responses API only maps onto openai-kind upstreams (chat.completions).
	// An anthropic or gemini upstream cannot be answered in the Responses
	// envelope, so reject rather than emit an unparseable response.
	if clientFmt == fmtResponses && (kind == "anthropic" || kind == "gemini") {
		h.Router.ReportFailure(p, router.ErrClient)
		*lastErr = fmt.Errorf("provider %s (kind=anthropic) cannot serve a Responses API request", p.Provider.Name)
		return false
	}
	if crossKind {
		// Cross-kind requests are translated request-side (payload) and,
		// when streaming, response-side by translateStream inside streamRelay
		// (the old 501 is replaced by in-flight SSE translation). Both the
		// request body and the response event stream are rewritten into the
		// provider's / client's dialect respectively.
		translated, terr := translateBody(api, kindOf(kind), processed)
		if terr != nil {
			h.Router.ReportFailure(p, router.ErrServer)
			*lastErr = terr
			return false
		}
		payload = translated
		// The translation re-emits the client's original model string; the
		// candidate's upstream model must win there too (provider-prefixed
		// or free-variant names are neither valid upstream IDs nor the
		// listed name).
		if cand.Upstream != requested {
			if rewritten, rerr := rewriteModel(payload, cand.Upstream); rerr == nil {
				payload = rewritten
			}
		}
	}

	// Anthropic-bound prompt caching (opt-in): inject cache_control
	// breakpoints into the final outbound /v1/messages body (works for
	// both same-kind and cross-kind anthropic candidates) so repeat
	// agentic prefixes are billed at the cache-read rate. Strictly
	// additive; never rewrites an existing breakpoint. Applied after model
	// rewrite so it sees the final outbound bytes.
	if kind == "anthropic" && h.Cfg.Get().Cache.PromptCache {
		payload = injectPromptCache(payload)
	}

	status, respBody, ct, retryAfter, susage, rerr := h.relay(attemptCtx, w, p.Provider.BaseURL, r, payload, streaming, kind, p.Provider.APIKeyEnv, api, clientFmt)

	if rerr != nil {
		// Network/transport failure: retryable (unless mid-stream).
		if router.IsStreamAborted(rerr) {
			// Client already received bytes; failover would duplicate.
			return true
		}
		class := router.Classify(rerr)
		if !router.IsRetryableClass(class) {
			h.Metrics.Request(client, p.Provider.Name, requested, "error")
			logReq(reqlog.Entry{Client: client, Model: requested, Provider: p.Provider.Name, Stream: streaming, Status: http.StatusBadGateway, Class: "error"})
			writeJSON(w, http.StatusBadGateway, map[string]any{
				"error": map[string]any{"message": rerr.Error(), "type": "upstream_error"},
			})
			return true
		}
		h.Metrics.Failure(p.Provider.Name, class.String())
		h.Router.ReportFailureWithBackoff(p, class, retryAfter)
		*lastErr = rerr
		h.Logger.Printf("provider %s failed (%v): %v; failing over", p.Provider.Name, class, rerr)
		return false
	}

	if streaming && status >= 200 && status < 300 {
		// relay already streamed the response to the client. Token usage is
		// captured from the SSE stream by the usage sniffer in streamRelay
		// (same-kind and cross-kind), never buffering the response.
		// A non-2xx was returned by relayStream as (status, body, ...)
		// with nothing streamed yet, so it falls through to the shared
		// error handling below (classify, report failure, fail over).
		h.Router.ReportSuccess(p)
		prompt := susage.prompt
		completion := susage.completion
		if prompt == 0 {
			prompt = int64(tokenize.Count(string(processed), tokenize.KindOpenAI))
		}
		h.Usage.Record(client, modelFromBody(body), prompt, completion, int64(rtkSaved), 0, pricesOf(h.Cfg.Get(), p.Provider.Name), 0)
		h.Metrics.Request(client, p.Provider.Name, requested, "ok")
		logReq(reqlog.Entry{Client: client, Model: requested, UpstreamModel: cand.Upstream, Provider: p.Provider.Name, Stream: true, Status: http.StatusOK, Class: "ok", PromptTokens: int64(prompt), RTKSavedTokens: int64(rtkSaved)})
		return true
	}

	if !streaming && status >= 200 && status < 300 {
		h.Router.ReportSuccess(p)
		if ct == "" {
			ct = "application/json"
		}
		// Responses API client: wrap the chat envelope in the Responses
		// response shape before caching/writing. Gemini upstream: translate
		// the generateContent response back to OpenAI first (this also lets
		// usageFromBody read Gemini's usageMetadata via the OpenAI shape).
		sendBody := respBody
		if kind == "gemini" {
			if gb, gerr := geminiToOpenAI(respBody, modelFromBody(body)); gerr == nil {
				respBody = gb
				sendBody = gb
			}
		}
		if clientFmt == fmtResponses {
			if wrapped, werr := openAIToResponses(respBody, requested); werr == nil {
				sendBody = wrapped
			}
		}
		// Usage: parse provider-reported tokens when present; fall back
		// to estimates. RTK savings land on the provider's row.
		prompt, completion, reportedCost, cacheRead := usageFromBody(respBody, body)
		h.Usage.RecordFull(client, modelFromBody(body), prompt, completion, int64(rtkSaved), 0, cacheRead, pricesOf(h.Cfg.Get(), p.Provider.Name), reportedCost)
		h.Cache.Put(cacheKey(processed), cacheEntry(respBody, ct, prompt, completion))
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
		_, _ = w.Write(sendBody)
		return true
	}

	// Upstream error status. Credits failures (402, or 401 with a
	// credits body) fail over WITHOUT cooldown escalation — the provider
	// may still serve free variants.
	class := router.ClassifyStatusBody(status, respBody)

	// 401/403 auth failure: before failing over, attempt a credential
	// refresh (the API key may have rotated in routre-cli.env). If the key
	// is stale and a fresh one changes the picture, retry this candidate
	// once with the refreshed key. Only when refresh is a no-op or still
	// rejects does it fail over like any other retryable failure.
	if class == router.ErrAuth {
		if refreshed := h.refreshCredentials(p.Provider.APIKeyEnv); refreshed {
			// The key changed underneath us: retry this candidate once with
			// the new key. Reports both success and failure to the router on
			// the retry path — a successful auth retry must clear any
			// cooldown this provider accrued from past failures.
			if h.tryCandidate(ctx, w, r, api, cand, requested, body, processed, streaming, client, rtkSaved, clientFmt, lastErr, logReq) {
				return true
			}
		}
	}

	if !router.IsRetryableClass(class) {
		// Client-caused (400/404/422...): surface it, no failover.
		h.Metrics.Request(client, p.Provider.Name, requested, "client_error")
		logReq(reqlog.Entry{Client: client, Model: requested, Provider: p.Provider.Name, Stream: streaming, Status: status, Class: "client_error"})
		writeStatus(w, status, respBody, ct)
		return true
	}
	h.Metrics.Failure(p.Provider.Name, class.String())
	h.Router.ReportFailureWithBackoff(p, class, retryAfter)
	*lastErr = fmt.Errorf("provider %s: status %d (%v)", p.Provider.Name, status, class)
	h.Logger.Printf("provider %s status %d (%v); failing over", p.Provider.Name, status, class)
	return false
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

// cacheEntry wraps a response for the cache, carrying the upstream-reported
// token usage so later hits report provider-accurate numbers.
func cacheEntry(body []byte, ct string, prompt, completion int64) cache.Entry {
	return cache.Entry{Body: body, ContentType: ct, PromptTokens: prompt, CompletionTokens: completion}
}

// kindOf maps a provider kind string to an apiFormat.
func kindOf(kind string) apiFormat {
	if kind == "anthropic" {
		return fmtAnthropic
	}
	if kind == "gemini" {
		return fmtGemini
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

// buildUpstreamRequest prepares an upstream *http.Request shared by the
// streaming and non-streaming relay paths, applying the provider key and
// passthrough headers. It does NOT send the request or read the response.
// streaming selects whether the Accept header defaults to text/event-stream
// (the non-streaming path sends no Accept when the client sent none).
func (h *Handlers) buildUpstreamRequest(ctx context.Context, baseURL, kind, path string, payload []byte, r *http.Request, apiKeyEnv string, streaming bool) (*http.Request, error) {
	base := strings.TrimRight(baseURL, "/")
	if strings.HasSuffix(base, "/v1") {
		// Base already includes the /v1 prefix (OpenAI-style base URLs).
		path = strings.TrimPrefix(path, "/v1")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	// Upstreams that sniff Accept for SSE negotiation should see an explicit
	// event-stream intent; default it when the client sent none on the
	// streaming path.
	if streaming {
		if accept := r.Header.Get("Accept"); accept == "" {
			req.Header.Set("Accept", "text/event-stream")
		} else {
			req.Header.Set("Accept", accept)
		}
	}
	// Provider API key: the gateway holds the key (from api_key_env), NOT
	// the client. A client-sent Authorization header is only a placeholder
	// (many CLIs require one); it must never reach the upstream.
	providerKey, missing := h.providerKey(apiKeyEnv)
	if missing {
		return nil, fmt.Errorf("provider key %s is not set (use `routre-cli setup` or export it)", apiKeyEnv)
	}
	if kind == "anthropic" {
		req.Header.Set("X-Api-Key", providerKey)
		req.Header.Set("Anthropic-Version", firstNonEmpty(r.Header.Get("Anthropic-Version"), "2023-06-01"))
	} else {
		req.Header.Set("Authorization", "Bearer "+providerKey)
	}
	// Provider-specific passthroughs (identical on both paths so beta-gated
	// features behave the same streaming and non-streaming).
	for _, hdr := range []string{"Anthropic-Version", "Anthropic-Beta", "OpenAI-Beta"} {
		if v := r.Header.Get(hdr); v != "" {
			req.Header.Set(hdr, v)
		}
	}
	return req, nil
}

// relay performs the actual upstream call.
// For non-streaming it returns (status, body, contentType, retryAfter, err).
// For streaming it streams SSE to w and returns (0, nil, "", retryAfter, err)
// where err is nil on success, retryable on pre-first-byte failure, or
// router.StreamAborted() after the first byte. from is the client's dialect,
// used for cross-kind streaming translation. retryAfter is the parsed
// upstream Retry-After delay (0 when absent) on a non-2xx response.
func (h *Handlers) relay(ctx context.Context, w http.ResponseWriter, baseURL string, r *http.Request, payload []byte, streaming bool, kind, apiKeyEnv string, from apiFormat, clientFmt apiFormat) (int, []byte, string, time.Duration, streamUsage, error) {
	if streaming {
		return h.relayStream(ctx, w, baseURL, r, payload, kind, apiKeyEnv, clientFmt)
	}
	path := "/v1/chat/completions"
	if kind == "anthropic" {
		path = "/v1/messages"
	}
	if kind == "gemini" {
		path = "/v1beta/models/" + modelFromBody(payload) + ":generateContent"
	}
	req, err := h.buildUpstreamRequest(ctx, baseURL, kind, path, payload, r, apiKeyEnv, false)
	if err != nil {
		return 0, nil, "", 0, streamUsage{}, err
	}

	resp, err := h.HTTPClient.Do(req)
	if err != nil {
		return 0, nil, "", 0, streamUsage{}, err
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
		return 0, nil, "", 0, streamUsage{}, fmt.Errorf("read upstream response: %w", err)
	}
	return resp.StatusCode, body, resp.Header.Get("Content-Type"), parseRetryAfter(resp.Header.Get("Retry-After")), streamUsage{}, nil
}

// relayStream streams an SSE response to the client, copying upstream
// headers. On success it returns (http.StatusOK, nil, "", nil) and the
// caller must not write anything further — the status reports that the
// stream was relayed, while upstream 4xx/5xx are returned as
// (status, body, ct, nil) with nothing written yet so the caller can
// classify, report the failure, and fail over. Errors before the first
// byte are retryable; errors after the first byte are
// router.StreamAborted().
func (h *Handlers) relayStream(ctx context.Context, w http.ResponseWriter, baseURL string, r *http.Request, payload []byte, kind, apiKeyEnv string, from apiFormat) (int, []byte, string, time.Duration, streamUsage, error) {
	path := "/v1/chat/completions"
	if kind == "anthropic" {
		path = "/v1/messages"
	}
	if kind == "gemini" {
		path = "/v1beta/models/" + modelFromBody(payload) + ":generateContent?alt=sse"
	}
	req, err := h.buildUpstreamRequest(ctx, baseURL, kind, path, payload, r, apiKeyEnv, true)
	if err != nil {
		return 0, nil, "", 0, streamUsage{}, err
	}

	resp, err := h.HTTPClient.Do(req)
	if err != nil {
		return 0, nil, "", 0, streamUsage{}, err
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
			return 0, nil, "", 0, streamUsage{}, rerr
		}
		return resp.StatusCode, body, resp.Header.Get("Content-Type"), parseRetryAfter(resp.Header.Get("Retry-After")), streamUsage{}, nil
	}

	susage, serr := h.streamRelay(w, resp, from, kindOf(kind))
	if serr != nil {
		return 0, nil, "", 0, streamUsage{}, serr
	}
	return http.StatusOK, nil, "", 0, susage, nil
}

// streamRelay copies an SSE stream to the client, translating it when the
// client and upstream speak different dialects, and returns the token usage
// captured from the stream. flushAfter counts bytes written so far; on
// first-byte success the error is wrapped as a stream abort (failover must
// not retry).
func (h *Handlers) streamRelay(w http.ResponseWriter, resp *http.Response, from, to apiFormat) (streamUsage, error) {
	// Copy headers from upstream.
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set("X-Llrouter-Streaming", "true")
	w.WriteHeader(resp.StatusCode)

	flusher, _ := w.(http.Flusher)

	// Cross-kind: run the SSE state-machine translator before writing to the
	// client. It never buffers the whole response and never emits a byte until
	// the first parseable frame, preserving the failover-before-first-byte rule.
	sniffer := newUsageSniffer(resp.Body)
	if from != to {
		err := translateStream(w, sniffer, from, to, func() {
			if flusher != nil {
				flusher.Flush()
			}
		})
		sniffer.drainCarry()
		if err != nil {
			// Failed before any byte reached the client: retryable.
			return streamUsage{}, err
		}
		return sniffer.usage(), nil
	}

	// Same-kind OpenAI: guarantee the client always receives a terminal
	// chunk carrying finish_reason. Some providers (opencode.ai's
	// gpt-5.6-luna) close the stream after content without ever sending
	// finish_reason; strict clients then error with "Stream ended without
	// finish_reason". We parse frames and synthesize the chunk when the
	// upstream omits it. Anthropic clients have no finish_reason contract,
	// so they keep the raw byte-copy path below.
	if to == fmtOpenAI {
		err := relayOpenAIGuaranteeFinish(w, sniffer, func() {
			if flusher != nil {
				flusher.Flush()
			}
		})
		if err != nil {
			return streamUsage{}, err
		}
		return sniffer.usage(), nil
	}

	buf := make([]byte, 32*1024)
	written := int64(0)
	firstByte := true
	for {
		n, rerr := sniffer.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				// Client went away mid-stream: not an upstream failure.
				return streamUsage{}, nil
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
				sniffer.drainCarry()
				return sniffer.usage(), nil
			}
			if firstByte {
				// Failed before any byte reached the client: retryable.
				return streamUsage{}, fmt.Errorf("upstream stream failed before first byte: %w", rerr)
			}
			return streamUsage{}, router.StreamAborted()
		}
	}
}

// syntheticFinishChunk is a minimal OpenAI streaming terminal chunk that
// carries finish_reason ("stop"); injected when the upstream ended the stream
// without sending one.
var syntheticFinishChunk = []byte(`data: {"object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n")

// relayOpenAIGuaranteeFinish reads the upstream SSE stream and writes it to
// the client frame-by-frame, guaranteeing that a chunk carrying finish_reason
// is emitted before [DONE]/EOF. If the upstream already sent one, bytes pass
// through unchanged. Otherwise a synthetic terminal chunk is injected.
func relayOpenAIGuaranteeFinish(w io.Writer, upstream io.Reader, flush func()) error {
	br := bufio.NewReader(upstream)
	sawFinish := false
	for {
		raw, ok, err := readRawFrame(br)
		if ok {
			sawFinish = sawFinish || bytes.Contains(raw, []byte("\"finish_reason\":\""))
			// Inject before the upstream's [DONE] if finish_reason was never sent.
			if bytes.Contains(raw, []byte("[DONE]")) && !sawFinish {
				if _, werr := io.WriteString(w, string(syntheticFinishChunk)); werr != nil {
					return nil // client went away
				}
				sawFinish = true
			}
			if _, werr := w.Write(raw); werr != nil {
				return nil // client went away
			}
			if flush != nil {
				flush()
			}
		}
		if err != nil {
			if err == io.EOF {
				// Upstream closed without [DONE]: still guarantee finish_reason.
				if !sawFinish {
					if _, werr := io.WriteString(w, string(syntheticFinishChunk)); werr == nil && flush != nil {
						flush()
					}
				}
				return nil
			}
			return nil // upstream read error after bytes were already relayed
		}
	}
}

// readRawFrame reads one raw SSE frame from br (all lines up to and including
// the terminating blank line, or EOF). ok=false at a clean EOF with nothing
// read.
func readRawFrame(br *bufio.Reader) ([]byte, bool, error) {
	var buf []byte
	for {
		line, err := br.ReadString('\n')
		if line != "" {
			buf = append(buf, line...)
			if strings.TrimRight(line, "\r\n") == "" {
				return buf, true, nil // blank line ends the frame
			}
		}
		if err != nil {
			if err == io.EOF {
				if len(buf) > 0 {
					return buf, true, nil
				}
				return nil, false, io.EOF
			}
			return buf, false, err
		}
	}
}

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

// refreshCredentials re-reads the routre-cli.env key file and reports
// whether the provider's API key actually changed as a result. The keystore
// serializes concurrent refreshes under its own mutex and never mutates the
// process environment.
func (h *Handlers) refreshCredentials(apiKeyEnv string) bool {
	_, changed := h.Keys.Refresh(config.EnvFilePath(h.Cfg.Path()), apiKeyEnv)
	return changed
}

// injectPromptCache marks Anthropic cache breakpoints on an outbound
// /v1/messages body: the system prefix and the last message's final text
// block get cache_control {type:"ephemeral"}. It is strictly additive and
// fail-open — an already-present cache_control is never overwritten, and
// malformed input is returned unchanged. When the last message's content is
// a plain string (no block array), cache_control cannot be attached there,
// so only the system block is marked.
func injectPromptCache(body []byte) []byte {
	if !json.Valid(body) {
		return body
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return body
	}
	changed := false

	// Mark the system prefix: if an array, attach to the first text block;
	// if a plain string, wrap it into an array so the breakpoint is
	// expressible.
	if sys, ok := doc["system"]; ok {
		switch s := sys.(type) {
		case []any:
			changed = markFirstTextBlock(s) || changed
		case string:
			if s != "" {
				doc["system"] = []any{map[string]any{
					"type":          "text",
					"text":          s,
					"cache_control": map[string]any{"type": "ephemeral"},
				}}
				changed = true
			}
		}
	}

	// Mark the last message's content if it is a block array with a
	// markable text block.
	if msgs, ok := doc["messages"].([]any); ok && len(msgs) > 0 {
		if last, ok := msgs[len(msgs)-1].(map[string]any); ok {
			if content, ok := last["content"].([]any); ok {
				changed = markLastTextBlock(content) || changed
			}
		}
	}

	if !changed {
		return body
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return body
	}
	return out
}

// markFirstTextBlock attaches ephemeral cache_control to the first text
// block of a content array (the system prefix is always a stable cache
// prefix). Reports whether a block was marked; never overwrites an existing
// breakpoint.
func markFirstTextBlock(blocks []any) bool {
	for _, b := range blocks {
		bm, ok := b.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := bm["type"].(string); t != "text" {
			continue
		}
		if _, already := bm["cache_control"]; already {
			return false
		}
		bm["cache_control"] = map[string]any{"type": "ephemeral"}
		return true
	}
	return false
}

// markLastTextBlock attaches ephemeral cache_control to the final text
// block of a content array (marking the most recent turn is what makes an
// agentic session's rolling context cacheable). Success-path semantics
// mirror markFirstTextBlock.
func markLastTextBlock(blocks []any) bool {
	for i := len(blocks) - 1; i >= 0; i-- {
		bm, ok := blocks[i].(map[string]any)
		if !ok {
			continue
		}
		if t, _ := bm["type"].(string); t != "text" {
			continue
		}
		if _, already := bm["cache_control"]; already {
			return false
		}
		bm["cache_control"] = map[string]any{"type": "ephemeral"}
		return true
	}
	return false
}
