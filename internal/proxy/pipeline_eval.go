package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mariobgsp/routre/internal/proxy/dialect"
	"github.com/mariobgsp/routre/internal/router"
	"github.com/mariobgsp/routre/internal/tokenize"
)

// attemptTimeout bounds a single non-streaming upstream attempt. Streaming
// relays are exempt (they can legitimately run for minutes; the transport
// already bounds dial + response headers).
const attemptTimeout = 30 * time.Second

// isNativeResponses reports whether a base URL speaks /v1/responses natively
// (opencode.ai/zen does; openrouter/others don't).
func isNativeResponses(baseURL string) bool { return strings.Contains(baseURL, "opencode.ai") }

func (p *Pipeline) preparePayload(api apiFormat, clientFmt apiFormat, cand router.Candidate, requested string, processed []byte) ([]byte, error) {
	kind := cand.Provider.Provider.Kind
	if clientFmt == fmtResponses && (kind == "anthropic" || kind == "gemini") {
		return nil, fmt.Errorf("provider %s (kind=%s) cannot serve a Responses API request", cand.Provider.Provider.Name, kind)
	}
	// Adaptable native passthrough: sanitize caller-bound Responses state
	// upfront (reasoning/previous_response_id), then model rewrite only.
	if clientFmt == fmtResponses && isNativeResponses(cand.Provider.Provider.BaseURL) {
		return cand.Payload(sanitizeResponsesPayload(processed), requested), nil
	}
	if clientFmt == fmtResponses {
		// Non-native provider: translate Responses -> chat before relay.
		translated, terr := dialect.ResponsesToOpenAI(processed)
		if terr != nil {
			return nil, terr
		}
		payload := cand.Payload(translated, requested)
		payload = clampPayload(payload, cand.Provider.Provider.MaxTokens)
		if kind == "anthropic" && p.cfg.Get().Cache.PromptCache {
			payload = injectPromptCache(payload)
		}
		return payload, nil
	}
	payload := cand.Payload(processed, requested)
	if crossKindRequest(api, kind) {
		translated, terr := p.d.Request(dialect.Format(api), dialect.KindToFormat(kind), processed)
		if terr != nil {
			return nil, terr
		}
		payload = translated
		if cand.Upstream != requested {
			if rewritten, rerr := rewriteModel(payload, cand.Upstream); rerr == nil {
				payload = rewritten
			}
		}
		payload = clampPayload(payload, cand.Provider.Provider.MaxTokens)
	}
	if kind == "anthropic" && p.cfg.Get().Cache.PromptCache {
		payload = injectPromptCache(payload)
	}
	return payload, nil
}

// mustJSON marshals v, or "null" for the failures.Outcome[] body.
func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}

// crossKindRequest reports whether inbound dialect `api` differs from the
// upstream provider's dialect (Responses handled separately).
func crossKindRequest(api apiFormat, kind string) bool {
	return api != fmtResponses && api != apiFormat(dialect.KindToFormat(kind))
}

func (p *Pipeline) tryEval(ctx context.Context, cand router.Candidate, req Request, api apiFormat, requested string, body []byte, env *envelope, streaming bool, client string, clientFmt apiFormat) evalResult {
	processed := env.body
	payload, perr := p.preparePayload(api, clientFmt, cand, requested, processed)
	if perr != nil {
		p.router.ReportFailure(cand.Provider, router.ErrClient)
		return evalResult{Err: perr, Class: router.ErrClient, Retryable: false}
	}
	kind := cand.Provider.Provider.Kind
	ph := req.Header
	dummyReq := &http.Request{Header: ph}
	attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
	defer cancel()
	rec := &responseRecorder{header: make(http.Header)}
	relayStart := time.Now()
	status, respBody, ct, retryAfter, _, rerr := p.handlers.relay(attemptCtx, rec, cand.Provider.Provider.BaseURL, dummyReq, payload, streaming, kind, cand.Provider.Provider.APIKeyEnv, api, clientFmt)
	if rerr == nil && clientFmt == fmtResponses && isReasoningStateError(status, respBody) {
		if sanitized := sanitizeResponsesPayload(processed); string(sanitized) != string(processed) {
			if sp, serr := p.preparePayload(api, clientFmt, cand, requested, sanitized); serr == nil {
				debugf("reasoning-state retry for %q", requested)
				rec = &responseRecorder{header: make(http.Header)}
				status, respBody, ct, retryAfter, _, rerr = p.handlers.relay(attemptCtx, rec, cand.Provider.Provider.BaseURL, dummyReq, sp, streaming, kind, cand.Provider.Provider.APIKeyEnv, api, clientFmt)
			}
		}
	}
	relayDur := time.Since(relayStart).Milliseconds()
	p.lastPhases = &Phases{TotalMS: relayDur}
	if rerr != nil {
		if router.IsStreamAborted(rerr) {
			return evalResult{OK: true, Err: rerr, Class: router.ErrStream}
		}
		class := router.Classify(rerr)
		if !router.IsRetryableClass(class) {
			return evalResult{OK: true, Response: &Response{StatusCode: 502, Body: []byte(fmt.Sprintf(`{"error":{"message":%q,"type":"upstream_error"}}`, rerr.Error()))}, Err: rerr, Class: class, Retryable: false}
		}
		p.metrics.Failure(cand.Provider.Provider.Name, class.String())
		p.router.ReportFailureWithBackoff(cand.Provider, class, retryAfter)
		return evalResult{Err: rerr, Class: class, Retryable: true}
	}
	if status >= 200 && status < 300 {
		p.router.ReportSuccess(cand.Provider)
		if ct == "" {
			ct = "application/json"
		}
		sendBody := respBody
		if kind == "gemini" && clientFmt != fmtResponses {
			if clientFmt == fmtAnthropic {
				if ab, aerr := dialect.GeminiToAnthropic(respBody, modelFromBody(body)); aerr == nil {
					respBody = ab
					sendBody = ab
				}
			} else if gb, gerr := dialect.GeminiToOpenAI(respBody, modelFromBody(body)); gerr == nil {
				respBody = gb
				sendBody = gb
			}
		}
		if kind == "anthropic" && clientFmt == fmtOpenAI {
			if ab, aerr := dialect.AnthropicToOpenAIResponse(respBody); aerr == nil {
				respBody = ab
				sendBody = ab
			}
		}
		if clientFmt == fmtResponses && !isNativeResponses(cand.Provider.Provider.BaseURL) {
			if wrapped, werr := dialect.OpenAIToResponses(respBody, requested); werr == nil {
				sendBody = wrapped
			}
		}
		extractor := NewExtractor()
		prompt, completion, reportedCost, cacheRead, cacheCreation := extractor.ExtractNonStreaming(respBody, body)
		p.usage.RecordFull(client, modelFromBody(body), prompt, completion, int64(env.rtkSaved), 0, cacheRead, cacheCreation, pricesOf(p.cfg.Get(), cand.Provider.Provider.Name), reportedCost)
		// ponytail: cache the post-translation body for non-native, raw responses for native
		cacheBody := respBody
		if clientFmt == fmtResponses && !isNativeResponses(cand.Provider.Provider.BaseURL) {
			cacheBody = respBody
		}
		if cacheableRequest(clientFmt, processed) {
			p.cache.Put(env.key, cacheEntry(cacheBody, ct, prompt, completion))
		}
		p.metrics.Request(client, cand.Provider.Provider.Name, requested, "ok")
		p.metrics.CacheRead(cand.Provider.Provider.Name, cacheRead)
		p.metrics.CacheCreation(cand.Provider.Provider.Name, cacheCreation)
		hdr := http.Header{"Content-Type": []string{ct}, "X-Llrouter-Cache": []string{"miss"}, "X-Llrouter-Provider": []string{cand.Provider.Provider.Name}}
		if cand.IsFree {
			hdr.Set("X-Llrouter-Free", cand.Upstream)
		}
		return evalResult{OK: true, Response: &Response{StatusCode: status, Body: sendBody, ContentType: ct, Header: hdr, Provider: cand.Provider.Provider.Name}}
	}
	class := router.ClassifyStatusBody(status, respBody)
	errStatus := func() error {
		return fmt.Errorf("provider %s: status %d (%v)", cand.Provider.Provider.Name, status, class)
	}
	// Auth and billing failures are terminal for this candidate even though
	// both count as retryable classes: the runner refreshes auth itself,
	// and a balance never heals in 500ms (same-retry just burns a round).
	if class == router.ErrAuth {
		return evalResult{Err: errStatus(), Class: class, Retryable: false}
	}
	if class == router.ErrCredits {
		p.metrics.Failure(cand.Provider.Provider.Name, class.String())
		p.router.ReportFailureWithBackoff(cand.Provider, class, retryAfter)
		return evalResult{Err: errStatus(), Class: class, Retryable: false}
	}
	if !router.IsRetryableClass(class) {
		if cand.ShouldFailoverOnClientError() {
			return evalResult{Err: fmt.Errorf("provider %s rejected model %q (HTTP %d)", cand.Provider.Provider.Name, cand.Upstream, status), Class: class, Retryable: false}
		}
		return evalResult{OK: true, Response: &Response{StatusCode: status, Body: respBody, ContentType: ct}, Class: class, Retryable: false}
	}
	p.metrics.Failure(cand.Provider.Provider.Name, class.String())
	p.router.ReportFailureWithBackoff(cand.Provider, class, retryAfter)
	return evalResult{Err: errStatus(), Class: class, Retryable: true}
}

// clampPayload caps max_tokens in payload to ceiling, handling both OpenAI (max_tokens) and Gemini (generationConfig.maxOutputTokens) shapes.
func clampPayload(payload []byte, ceiling int64) []byte {
	if ceiling <= 0 {
		return payload
	}
	var doc map[string]any
	if err := json.Unmarshal(payload, &doc); err != nil {
		return payload
	}
	// OpenAI/Anthropic style: top-level max_tokens
	if mt, ok := doc["max_tokens"]; ok {
		var n int64
		switch v := mt.(type) {
		case float64:
			n = int64(v)
		case json.Number:
			n, _ = v.Int64()
		default:
			goto gemini
		}
		promptEst := int64(0)
		if msgs, ok := doc["messages"]; ok {
			if mb, err := json.Marshal(msgs); err == nil {
				promptEst = tokenize.ClampCount(string(mb))
			}
		}
		const margin = 512
		maxAllowed := ceiling - promptEst - margin
		if maxAllowed < 1024 {
			maxAllowed = 1024
		}
		if n > maxAllowed {
			doc["max_tokens"] = maxAllowed
			if out, err := json.Marshal(doc); err == nil {
				return out
			}
		}
	}
gemini:
	// Gemini style: generationConfig.maxOutputTokens
	if gc, ok := doc["generationConfig"].(map[string]any); ok {
		if mot, ok := gc["maxOutputTokens"]; ok {
			var n int64
			switch v := mot.(type) {
			case float64:
				n = int64(v)
			case json.Number:
				n, _ = v.Int64()
			default:
				return payload
			}
			if n > ceiling {
				gc["maxOutputTokens"] = ceiling
				if out, err := json.Marshal(doc); err == nil {
					return out
				}
			}
		}
	}
	return payload
}

type responseRecorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (r *responseRecorder) Header() http.Header         { return r.header }
func (r *responseRecorder) Write(b []byte) (int, error) { return r.body.Write(b) }
func (r *responseRecorder) WriteHeader(statusCode int)  { r.status = statusCode }
