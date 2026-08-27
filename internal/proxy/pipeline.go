package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mariobgsp/routre/internal/cache"
	"github.com/mariobgsp/routre/internal/config"
	"github.com/mariobgsp/routre/internal/keystore"
	"github.com/mariobgsp/routre/internal/metrics"
	"github.com/mariobgsp/routre/internal/proxy/dialect"
	"github.com/mariobgsp/routre/internal/proxy/failures"
	"github.com/mariobgsp/routre/internal/reqlog"
	"github.com/mariobgsp/routre/internal/router"
	"github.com/mariobgsp/routre/internal/rtk"
	"github.com/mariobgsp/routre/internal/tokenize"
	"github.com/mariobgsp/routre/internal/usage"
	"log"
)

// Pipeline is the deep module that hides the 7-step request pipeline
// Deep: small interface, large hidden behavior. Inject deps, don't reach into Handlers.
type Pipeline struct {
	handlers   *Handlers
	cfg        *config.Store
	router     *router.Router
	cache      *cache.Cache
	rtk        *rtk.RTK
	d          *dialect.Dialect
	httpClient *http.Client
	usage      *usage.Store
	metrics    *metrics.Metrics
	keys       *keystore.Store
	logger     *log.Logger
}

func NewPipeline(h *Handlers) *Pipeline {
	return &Pipeline{
		handlers:   h,
		cfg:        h.Cfg,
		router:     h.Router,
		cache:      h.Cache,
		rtk:        h.RTK,
		d:          dialect.New(),
		httpClient: h.HTTPClient,
		usage:      h.Usage,
		metrics:    h.Metrics,
		keys:       h.Keys,
		logger:     h.Logger,
	}
}

// NewPipelineWithDeps is the injectable constructor for tests (DIP).
func NewPipelineWithDeps(handlers *Handlers, cfg *config.Store, router *router.Router, cache *cache.Cache, rtk *rtk.RTK, d *dialect.Dialect, httpClient *http.Client, usage *usage.Store, metrics *metrics.Metrics, keys *keystore.Store, logger *log.Logger) *Pipeline {
	return &Pipeline{handlers: handlers, cfg: cfg, router: router, cache: cache, rtk: rtk, d: d, httpClient: httpClient, usage: usage, metrics: metrics, keys: keys, logger: logger}
}

type Request struct {
	Body   []byte
	Path   string
	Header http.Header
	Client string
}

type Response struct {
	StatusCode  int
	Body        []byte
	ContentType string
	Header      http.Header
	FromCache   bool
	Provider    string
}

func (p *Pipeline) Process(ctx context.Context, req Request) (Response, error) {
	return p.processInternal(ctx, req)
}

func (p *Pipeline) Stream(ctx context.Context, req Request, w http.ResponseWriter) error {
	body := req.Body
	path := req.Path
	client := req.Client
	header := req.Header
	api := apiFormat(dialect.DetectFormat(path, body))
	clientFmt := api
	if api == fmtResponses {
		translated, err := dialect.ResponsesToOpenAI(body)
		if err != nil {
			return err
		}
		body = translated
		api = fmtOpenAI
	}
	requested := modelFromBody(body)
	processed, rtkChanged := p.rtk.Apply(body)
	rtkSaved := 0
	if rtkChanged {
		rtkSaved = tokenize.Estimate(string(body)) - tokenize.Estimate(string(processed))
		p.metrics.RTKApplied()
	}
	p.metrics.RTKSaved(int64(rtkSaved))
	if cfg := p.cfg.Get(); cfg.Cache.PrefixOrder {
		processed = orderPrompt(processed)
	}
	// Streaming replay cache: serve an identical earlier stream from memory.
	// Entries are byte-identical client-dialect SSE captures, so tool-call
	// ids, finish_reason and [DONE] stay self-consistent by construction.
	if e, ok := p.cache.Get(cacheKey(processed)); ok && e.SSE {
		cacheSaved := e.PromptTokens
		if cacheSaved == 0 {
			cacheSaved = int64(tokenize.Count(string(processed), tokenize.KindOpenAI))
		}
		if cacheSaved > 0 {
			p.usage.Record(client, requested, 0, 0, 0, cacheSaved, usage.Prices{}, 0)
		}
		p.metrics.CacheHit()
		w.Header().Set("Content-Type", e.ContentType)
		w.Header().Set("X-Llrouter-Cache", "hit")
		w.Header().Set("X-Llrouter-Streaming", "true")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(e.Body)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		return nil
	}
	p.metrics.CacheMiss()
	cands := p.router.CandidatesWithFallbacks(requested, p.cfg.Get().Fallbacks)
	if len(cands) == 0 {
		// No candidate was even eligible (model unlisted or every serving
		// provider is in cooldown). Surface the reason on the wire so the
		// user can see it without a separate status call.
		if retryAfter, served := p.router.MinCooldownForModel(requested, p.cfg.Get().ForwardUnknown); served {
			failures.Render(w, failures.KindProvidersUnavailable, requested,
				[]failures.Outcome{{Provider: "*", Cooldown: retryAfter}}, retryAfter)
			return nil
		}
		failures.Render(w, failures.KindModelNotFound, requested, nil, 0)
		return nil
	}
	// Per-cand retry + auth refresh + tryLog accumulation stay inline
	// in this Stream path. The candidateRunner (internal/proxy/runner.go)
	// is a private struct that captures this exact policy; the eval
	// wiring is queued for a follow-up PR once the runner has its own
	// unit tests asserting the retry/refresh/Emitted contract.
	var lastErr error
	var lastClass router.ErrClass
	var tryLog []failures.Outcome
	for _, cand := range cands {
		attempts := 1 + retryTransientAttempts
		for attempt := 0; attempt < attempts; attempt++ {
			if attempt > 0 {
				if lastClass == router.ErrClient {
					break
				}
				time.Sleep(transientRetryDelay)
			}
			ok, err, class := p.streamCandidate(ctx, w, header, api, cand, requested, body, processed, client, rtkSaved, clientFmt)
			if ok && err == nil {
				return nil
			}
			if err != nil {
				lastErr = err
				lastClass = class
				if !router.IsRetryableClass(class) {
					break
				}
			}
		}
		entry := failures.Outcome{
			Provider: cand.Provider.Provider.Name,
			Kind:     cand.Provider.Provider.Kind,
			Class:    lastClass.String(),
		}
		if lastErr != nil {
			entry.Err = lastErr.Error()
		}
		if cd := p.router.CooldownRemaining(cand.Provider); cd > 0 {
			entry.Cooldown = cd
		}
		tryLog = append(tryLog, entry)
	}
	// Every candidate failed pre-first-byte (streamCandidate never wrote
	// anything to w in that case). Render the per-provider breakdown so
	// the user sees the reasons, not a generic "all providers failed".
	failures.Render(w, failures.KindAllFailed, requested, tryLog, 5*time.Second)
	return nil
}

func (p *Pipeline) streamCandidate(ctx context.Context, w http.ResponseWriter, header http.Header, api apiFormat, cand router.Candidate, requested string, body []byte, processed []byte, client string, rtkSaved int, clientFmt apiFormat) (bool, error, router.ErrClass) {
	kind := cand.Provider.Provider.Kind
	payload := cand.Payload(processed, requested)
	crossKind := crossKindRequest(api, kind)
	if clientFmt == fmtResponses && (kind == "anthropic" || kind == "gemini") {
		p.router.ReportFailure(cand.Provider, router.ErrClient)
		return false, fmt.Errorf("provider %s cannot serve Responses", cand.Provider.Provider.Name), router.ErrClient
	}
	if crossKind {
		translated, terr := p.d.Request(dialect.Format(api), dialect.KindToFormat(kind), processed)
		if terr != nil {
			p.router.ReportFailure(cand.Provider, router.ErrServer)
			return false, terr, router.ErrServer
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
	dummyReq := &http.Request{Header: header}
	// Streaming relays are exempt from the per-attempt timeout (they can run
	// for minutes); the transport bounds dial + response headers.
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	status, errBody, _, retryAfter, susage, rerr := p.handlers.relay(streamCtx, w, cand.Provider.Provider.BaseURL, dummyReq, payload, true, kind, cand.Provider.Provider.APIKeyEnv, api, clientFmt)
	if rerr != nil {
		if router.IsStreamAborted(rerr) {
			// Client already received bytes; failover would duplicate output.
			return true, nil, 0
		}
		class := router.Classify(rerr)
		p.metrics.Failure(cand.Provider.Provider.Name, class.String())
		if !router.IsRetryableClass(class) {
			return true, rerr, class
		}
		p.router.ReportFailureWithBackoff(cand.Provider, class, retryAfter)
		return false, rerr, class
	}
	if status >= 200 && status < 300 {
		p.router.ReportSuccess(cand.Provider)
		// Usage is captured from the SSE stream by the sniffer inside relay
		// (same-kind and cross-kind), never buffering the response.
		prompt := susage.prompt
		if prompt == 0 {
			prompt = int64(tokenize.Count(string(processed), tokenize.KindOpenAI))
		}
		p.usage.RecordFull(client, modelFromBody(body), prompt, susage.completion, int64(rtkSaved), 0, susage.cacheRead, pricesOf(p.cfg.Get(), cand.Provider.Provider.Name), 0)
		p.metrics.CacheRead(susage.cacheRead)
		p.metrics.Request(client, cand.Provider.Provider.Name, requested, "ok")
		// Streaming replay cache: store the exact client-dialect SSE bytes so
		// an identical later request replays without an upstream call.
		if len(susage.captured) > 0 {
			p.cache.Put(cacheKey(processed), cache.Entry{
				Body: susage.captured, ContentType: "text/event-stream",
				PromptTokens: susage.prompt, CompletionTokens: susage.completion,
				SSE: true,
			})
		}
		return true, nil, 0
	}
	class := router.ClassifyStatusBody(status, errBody)
	if class == router.ErrAuth {
		if refreshed := p.handlers.refreshCredentials(cand.Provider.Provider.APIKeyEnv); refreshed {
			if ok, rerr, c := p.streamCandidate(ctx, w, header, api, cand, requested, body, processed, client, rtkSaved, clientFmt); ok || rerr != nil {
				return ok, rerr, c
			}
		}
	}
	if !router.IsRetryableClass(class) {
		if cand.ShouldFailoverOnClientError() {
			return false, fmt.Errorf("provider %s rejected model %q (HTTP %d)", cand.Provider.Provider.Name, cand.Upstream, status), class
		}
		return true, fmt.Errorf("provider %s: status %d (%v)", cand.Provider.Provider.Name, status, class), class
	}
	p.metrics.Failure(cand.Provider.Provider.Name, class.String())
	p.router.ReportFailureWithBackoff(cand.Provider, class, retryAfter)
	return false, fmt.Errorf("provider %s: status %d (%v)", cand.Provider.Provider.Name, status, class), class
}

func (p *Pipeline) processInternal(ctx context.Context, req Request) (Response, error) {
	body := req.Body
	path := req.Path
	client := req.Client
	api := apiFormat(dialect.DetectFormat(path, body))
	clientFmt := api
	if api == fmtResponses {
		translated, err := dialect.ResponsesToOpenAI(body)
		if err != nil {
			return Response{StatusCode: 400, Body: []byte(fmt.Sprintf(`{"error":{"message":"could not parse Responses request: %v","type":"invalid_request_error"}}`, err))}, nil
		}
		body = translated
		api = fmtOpenAI
	}
	streaming := dialect.IsStreaming(body)
	requested := modelFromBody(body)
	processed, rtkChanged := p.rtk.Apply(body)
	rtkSaved := 0
	if rtkChanged {
		rtkSaved = tokenize.Estimate(string(body)) - tokenize.Estimate(string(processed))
		p.metrics.RTKApplied()
	}
	p.metrics.RTKSaved(int64(rtkSaved))
	if cfg := p.cfg.Get(); cfg.Cache.PrefixOrder {
		processed = orderPrompt(processed)
	}
	key := cacheKey(processed)
	if !streaming {
		if e, ok := p.cache.Get(key); ok && !e.SSE {
			cacheSaved := e.PromptTokens
			if cacheSaved == 0 {
				cacheSaved = int64(tokenize.Count(string(processed), tokenize.KindOpenAI))
			}
			if cacheSaved > 0 {
				p.usage.Record(client, modelFromBody(processed), 0, 0, 0, cacheSaved, usage.Prices{}, 0)
			}
			p.metrics.CacheHit()
			cacheBody := e.Body
			cacheCT := e.ContentType
			if clientFmt == fmtResponses {
				if wrapped, werr := dialect.OpenAIToResponses(e.Body, requested); werr == nil {
					cacheBody = wrapped
					cacheCT = "application/json"
				}
			}
			return Response{StatusCode: 200, Body: cacheBody, ContentType: cacheCT, Header: http.Header{"X-Llrouter-Cache": []string{"hit"}}, FromCache: true}, nil
		}
		p.metrics.CacheMiss()
	}
	cands := p.router.CandidatesWithFallbacks(requested, p.cfg.Get().Fallbacks)
	if len(cands) == 0 {
		if retryAfter, served := p.router.MinCooldownForModel(requested, p.cfg.Get().ForwardUnknown); served {
			if retryAfter < time.Second {
				retryAfter = time.Second
			}
			body, hdr := failures.RenderBody(failures.KindProvidersUnavailable, requested,
				[]failures.Outcome{{Provider: "*", Cooldown: retryAfter}}, retryAfter)
			return Response{StatusCode: 503, Body: body, Header: hdr}, nil
		}
		body, _ := failures.RenderBody(failures.KindModelNotFound, requested, nil, 0)
		return Response{StatusCode: 503, Body: body}, nil
	}
	if !streaming {
		var lastErr error
		var lastClass router.ErrClass
		var tryLog []failures.Outcome
		for _, cand := range cands {
			attempts := 1 + retryTransientAttempts
			for attempt := 0; attempt < attempts; attempt++ {
				if attempt > 0 {
					if lastClass == router.ErrClient {
						break
					}
					time.Sleep(transientRetryDelay)
				}
				resp, ok, err, class := p.tryCandidate(ctx, req, api, cand, requested, body, processed, streaming, client, rtkSaved, clientFmt)
				if err != nil {
					lastErr = err
					lastClass = class
				}
				if ok {
					return resp, nil
				}
				lastErr = err
				lastClass = class
			}
			entry := failures.Outcome{
				Provider: cand.Provider.Provider.Name,
				Kind:     cand.Provider.Provider.Kind,
				Class:    lastClass.String(),
			}
			if lastErr != nil {
				entry.Err = lastErr.Error()
			}
			if cd := p.router.CooldownRemaining(cand.Provider); cd > 0 {
				entry.Cooldown = cd
			}
			tryLog = append(tryLog, entry)
		}
		// Re-shape the 503 into a more honest status when the failure
		// pattern is uniform. If every candidate returned 4xx (the
		// provider doesn't serve this model), the user should see a
		// 404 model_not_found, not a 503 outage. If every candidate
		// returned 401/403, the user should see a 502 (their keys are
		// bad for this model), not a 503. Only the mixed / server-side
		// case stays as 503.
		allClient, allAuth := true, true
		for _, e := range tryLog {
			if e.Class != router.ErrClient.String() {
				allClient = false
			}
			if e.Class != router.ErrAuth.String() {
				allAuth = false
			}
		}
		switch {
		case allClient && len(tryLog) > 0:
			// Every provider rejected the model (400/404/422). This
			// is a model-not-found, not a service outage.
			body, _ := failures.RenderBody(failures.KindModelNotFound, requested, tryLog, 0)
			return Response{StatusCode: 404, Body: body, Header: http.Header{"Content-Type": []string{"application/json"}}, Provider: tryLog[0].Provider}, nil
		case allAuth && len(tryLog) > 0:
			// Every provider returned 401/403: the configured keys
			// don't authorize this model. Surface as 502 (bad
			// gateway) so the user knows the gateway is fine, the
			// auth is wrong.
			body := []byte(fmt.Sprintf(`{"error":{"message":"all configured providers rejected the request (auth). check API keys and model access.","type":"all_providers_unauthorized","model":%q,"attempts":%s}}`,
				requested, mustJSON(tryLog)))
			return Response{StatusCode: 502, Body: body, Header: http.Header{"Content-Type": []string{"application/json"}}, Provider: tryLog[0].Provider}, nil
		}
		// Mixed classes (some 5xx, some 4xx, some auth) — genuine
		// outage. Surface as 503 with the per-provider breakdown.
		prov := ""
		if len(tryLog) > 0 {
			prov = tryLog[0].Provider
		}
		body, hdr := failures.RenderBody(failures.KindAllFailed, requested, tryLog, 5*time.Second)
		return Response{StatusCode: 503, Body: body, Header: hdr, Provider: prov}, nil
	}
	return Response{}, fmt.Errorf("streaming request: use Stream")
}

// mustJSON marshals v or returns a literal null. Used for the
// all-auth response body where failures.Outcome[] must serialize.
func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}

// crossKindRequest reports whether an inbound request of dialect `api`
// targets an upstream provider of `kind` speaking a different dialect.
// Responses requests are handled separately (OpenAI-upstream only).
func crossKindRequest(api apiFormat, kind string) bool {
	return api != fmtResponses && api != kindOf(kind)
}

func (p *Pipeline) tryCandidate(ctx context.Context, req Request, api apiFormat, cand router.Candidate, requested string, body, processed []byte, streaming bool, client string, rtkSaved int, clientFmt apiFormat) (Response, bool, error, router.ErrClass) {
	ph := req.Header
	dummyReq := &http.Request{Header: ph}
	payload := cand.Payload(processed, requested)
	kind := cand.Provider.Provider.Kind
	crossKind := crossKindRequest(api, kind)
	if clientFmt == fmtResponses && (kind == "anthropic" || kind == "gemini") {
		p.router.ReportFailure(cand.Provider, router.ErrClient)
		return Response{}, false, fmt.Errorf("provider %s (kind=%s) cannot serve a Responses API request", cand.Provider.Provider.Name, kind), router.ErrClient
	}
	if crossKind {
		translated, terr := p.d.Request(dialect.Format(api), dialect.KindToFormat(kind), processed)
		if terr != nil {
			p.router.ReportFailure(cand.Provider, router.ErrServer)
			return Response{}, false, terr, router.ErrServer
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
	attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
	defer cancel()
	rec := &responseRecorder{header: make(http.Header)}
	status, respBody, ct, retryAfter, _, rerr := p.handlers.relay(attemptCtx, rec, cand.Provider.Provider.BaseURL, dummyReq, payload, streaming, kind, cand.Provider.Provider.APIKeyEnv, api, clientFmt)
	if rerr != nil {
		if router.IsStreamAborted(rerr) {
			return Response{}, true, nil, router.ErrStream
		}
		class := router.Classify(rerr)
		if !router.IsRetryableClass(class) {
			return Response{StatusCode: 502, Body: []byte(fmt.Sprintf(`{"error":{"message":%q,"type":"upstream_error"}}`, rerr.Error()))}, true, rerr, class
		}
		p.metrics.Failure(cand.Provider.Provider.Name, class.String())
		p.router.ReportFailureWithBackoff(cand.Provider, class, retryAfter)
		return Response{}, false, rerr, class
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
		if clientFmt == fmtResponses {
			if wrapped, werr := dialect.OpenAIToResponses(respBody, requested); werr == nil {
				sendBody = wrapped
			}
		}
		extractor := NewExtractor()
		prompt, completion, reportedCost, cacheRead := extractor.ExtractNonStreaming(respBody, body)
		p.usage.RecordFull(client, modelFromBody(body), prompt, completion, int64(rtkSaved), 0, cacheRead, pricesOf(p.cfg.Get(), cand.Provider.Provider.Name), reportedCost)
		p.cache.Put(cacheKey(processed), cacheEntry(respBody, ct, prompt, completion))
		p.metrics.Request(client, cand.Provider.Provider.Name, requested, "ok")
		p.metrics.CacheRead(cacheRead)
		hdr := http.Header{"Content-Type": []string{ct}, "X-Llrouter-Cache": []string{"miss"}, "X-Llrouter-Provider": []string{cand.Provider.Provider.Name}}
		if cand.IsFree {
			hdr.Set("X-Llrouter-Free", cand.Upstream)
		}
		return Response{StatusCode: status, Body: sendBody, ContentType: ct, Header: hdr, Provider: cand.Provider.Provider.Name}, true, nil, 0
	}
	class := router.ClassifyStatusBody(status, respBody)
	if class == router.ErrAuth {
		if refreshed := p.handlers.refreshCredentials(cand.Provider.Provider.APIKeyEnv); refreshed {
			if resp, ok, err, c := p.tryCandidate(ctx, req, api, cand, requested, body, processed, streaming, client, rtkSaved, clientFmt); ok {
				return resp, ok, err, c
			}
		}
	}
	if !router.IsRetryableClass(class) {
		if cand.ShouldFailoverOnClientError() {
			return Response{}, false, fmt.Errorf("provider %s rejected model %q (HTTP %d)", cand.Provider.Provider.Name, cand.Upstream, status), class
		}
		return Response{StatusCode: status, Body: respBody, ContentType: ct}, true, nil, class
	}
	p.metrics.Failure(cand.Provider.Provider.Name, class.String())
	p.router.ReportFailureWithBackoff(cand.Provider, class, retryAfter)
	return Response{}, false, fmt.Errorf("provider %s: status %d (%v)", cand.Provider.Provider.Name, status, class), class
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
				promptEst = int64(tokenize.Count(string(mb), tokenize.KindOpenAI))
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

var _ = json.Marshal
var _ = strings.Contains
var _ = io.Copy
var _ = reqlog.Write
