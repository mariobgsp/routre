package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/mariobgsp/routre/internal/cache"
	"github.com/mariobgsp/routre/internal/config"
	"github.com/mariobgsp/routre/internal/keystore"
	"github.com/mariobgsp/routre/internal/metrics"
	"github.com/mariobgsp/routre/internal/proxy/dialect"
	"github.com/mariobgsp/routre/internal/proxy/failures"
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
	lastPhases *Phases
}

// LastPhases returns the per-phase timing from the most recent
// upstream attempt, or nil if the request was served from cache or
// eval didn't measure. Read once, immediately after Stream/Process
// returns.
func (p *Pipeline) LastPhases() *Phases { return p.lastPhases }

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
	p.lastPhases = nil
	body := req.Body
	path := req.Path
	client := req.Client
	header := req.Header
	api := apiFormat(dialect.DetectFormat(path, body))
	debugf("stream request %q client=%q api=%v", modelFromBody(body), client, api)
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
		rtkSaved = tokenize.Count(string(body), tokenize.KindOpenAI) - tokenize.Count(string(processed), tokenize.KindOpenAI)
		p.metrics.RTKApplied()
	}
	p.metrics.RTKSaved(int64(rtkSaved))
	if cfg := p.cfg.Get(); cfg.Cache.PrefixOrder {
		processed = orderPrompt(processed)
	}
	// Streaming replay cache: serve an identical earlier stream from memory.
	// Entries are byte-identical client-dialect SSE captures, so tool-call
	// ids, finish_reason and [DONE] stay self-consistent by construction.
	streamKey := p.keyFor(processed)
	e, got, missReason := p.cache.GetWithReason(streamKey)
	if got && e.SSE {
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
	if got && !e.SSE {
		// Entry exists but is a JSON (non-streaming) capture; the client
		// asked for a stream. This is a shape mismatch, not a capacity/age
		// miss.
		p.metrics.CacheMissReason("shape_mismatch")
	} else {
		p.metrics.CacheMissReason(string(missReason))
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
	// Per-cand retry + auth refresh + tryLog accumulation now flow
	// through candidateRunner (internal/proxy/runner.go). The eval
	// closure below is the per-attempt work; the runner owns the
	// iteration policy.
	runner := newRunner(p.router, p.handlers.refreshCredentials)
	result := runner.Run(ctx, cands, func(ctx context.Context, cand router.Candidate, attempt int) evalResult {
		return p.streamEval(ctx, cand, attempt, w, header, api, requested, body, processed, client, rtkSaved, clientFmt)
	})
	if result.OK {
		return nil
	}
	allOverloaded := len(result.TryLog) > 0
	for _, e := range result.TryLog {
		if e.Class != router.ErrOverloaded.String() {
			allOverloaded = false
			break
		}
	}
	if allOverloaded {
		debugf("all overloaded (stream) for %q, retry after 1s", requested)
		time.Sleep(time.Second)
		retry := runner.Run(ctx, cands, func(ctx context.Context, cand router.Candidate, attempt int) evalResult {
			return p.streamEval(ctx, cand, attempt, w, header, api, requested, body, processed, client, rtkSaved, clientFmt)
		})
		if retry.OK {
			return nil
		}
		if len(retry.TryLog) > 0 {
			stillOverloaded := true
			for _, e := range retry.TryLog {
				if e.Class != router.ErrOverloaded.String() {
					stillOverloaded = false
					break
				}
			}
			if stillOverloaded {
				debugf("still overloaded (stream) for %q, second retry after 1s", requested)
				time.Sleep(time.Second)
				retry2 := runner.Run(ctx, cands, func(ctx context.Context, cand router.Candidate, attempt int) evalResult {
					return p.streamEval(ctx, cand, attempt, w, header, api, requested, body, processed, client, rtkSaved, clientFmt)
				})
				if retry2.OK {
					return nil
				}
				result = retry2
			} else {
				result = retry
			}
		} else {
			result = retry
		}
	}
	failures.Render(w, failures.KindAllFailed, requested, result.TryLog, 5*time.Second)
	return nil
}

// streamEval is the per-attempt streaming eval passed to candidateRunner.
// It performs prep + the upstream relay, records success/failure
// side effects, and tells the runner whether to stop (OK), retry
// (Retryable + retryable class), or move to the next candidate.
func (p *Pipeline) streamEval(ctx context.Context, cand router.Candidate, _ int, w http.ResponseWriter, header http.Header, api apiFormat, requested string, body, processed []byte, client string, rtkSaved int, clientFmt apiFormat) evalResult {
	payload, perr := p.preparePayload(api, clientFmt, cand, requested, processed)
	if perr != nil {
		p.router.ReportFailure(cand.Provider, router.ErrClient)
		return evalResult{Err: perr, Class: router.ErrClient, Retryable: false}
	}
	kind := cand.Provider.Provider.Kind
	dummyReq := &http.Request{Header: header}
	// Streaming relays are exempt from the per-attempt timeout (they can run
	// for minutes); the transport bounds dial + response headers.
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	relayStart := time.Now()
	status, errBody, _, retryAfter, susage, rerr := p.handlers.relay(streamCtx, w, cand.Provider.Provider.BaseURL, dummyReq, payload, true, kind, cand.Provider.Provider.APIKeyEnv, api, clientFmt)
	relayDur := time.Since(relayStart).Milliseconds()
	p.lastPhases = &Phases{TotalMS: relayDur}
	if rerr != nil {
		if router.IsStreamAborted(rerr) {
			// Client already received bytes; failover would duplicate output.
			return evalResult{OK: true, Err: rerr, Class: router.ErrStream, Emitted: true}
		}
		class := router.Classify(rerr)
		p.metrics.Failure(cand.Provider.Provider.Name, class.String())
		if !router.IsRetryableClass(class) {
			return evalResult{OK: true, Err: rerr, Class: class, Retryable: false, Emitted: true}
		}
		p.router.ReportFailureWithBackoff(cand.Provider, class, retryAfter)
		return evalResult{Err: rerr, Class: class, Retryable: true}
	}
	if status >= 200 && status < 300 {
		p.router.ReportSuccess(cand.Provider)
		// Usage is captured from the SSE stream by the sniffer inside relay
		// (same-kind and cross-kind), never buffering the response.
		prompt := susage.prompt
		if prompt == 0 {
			prompt = int64(tokenize.Count(string(processed), tokenize.KindOpenAI))
		}
		p.usage.RecordFull(client, modelFromBody(body), prompt, susage.completion, int64(rtkSaved), 0, susage.cacheRead, susage.cacheCreation, pricesOf(p.cfg.Get(), cand.Provider.Provider.Name), 0)
		p.metrics.CacheRead(cand.Provider.Provider.Name, susage.cacheRead)
		p.metrics.CacheCreation(cand.Provider.Provider.Name, susage.cacheCreation)
		p.metrics.Request(client, cand.Provider.Provider.Name, requested, "ok")
		// Streaming replay cache: store the exact client-dialect SSE bytes so
		// an identical later request replays without an upstream call.
		if len(susage.captured) > 0 {
			p.cache.Put(p.keyFor(processed), cache.Entry{
				Body: susage.captured, ContentType: "text/event-stream",
				PromptTokens: susage.prompt, CompletionTokens: susage.completion,
				SSE: true,
			})
		}
		return evalResult{OK: true}
	}
	class := router.ClassifyStatusBody(status, errBody)
	if class == router.ErrAuth {
		// Auth-refresh-and-retry: the runner will call refreshFn
		// on ErrAuth and re-invoke eval.
		return evalResult{Err: fmt.Errorf("provider %s: status %d (%v)", cand.Provider.Provider.Name, status, class), Class: class, Retryable: false}
	}
	if !router.IsRetryableClass(class) {
		if cand.ShouldFailoverOnClientError() {
			return evalResult{Err: fmt.Errorf("provider %s rejected model %q (HTTP %d)", cand.Provider.Provider.Name, cand.Upstream, status), Class: class, Retryable: false}
		}
		return evalResult{OK: true, Err: fmt.Errorf("provider %s: status %d (%v)", cand.Provider.Provider.Name, status, class), Class: class, Retryable: false, Emitted: false}
	}
	p.metrics.Failure(cand.Provider.Provider.Name, class.String())
	p.router.ReportFailureWithBackoff(cand.Provider, class, retryAfter)
	return evalResult{Err: fmt.Errorf("provider %s: status %d (%v)", cand.Provider.Provider.Name, status, class), Class: class, Retryable: true}
}

// preparePayload produces the wire body for one candidate: cross-kind
// translate, model rewrite, max_tokens clamp, and the Anthropic
// prompt-cache injection. Pulled out of the per-attempt evals so
// streaming and non-streaming share the same per-cand prep.
func (p *Pipeline) preparePayload(api apiFormat, clientFmt apiFormat, cand router.Candidate, requested string, processed []byte) ([]byte, error) {
	kind := cand.Provider.Provider.Kind
	if clientFmt == fmtResponses && (kind == "anthropic" || kind == "gemini") {
		return nil, fmt.Errorf("provider %s (kind=%s) cannot serve a Responses API request", cand.Provider.Provider.Name, kind)
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

// keyFor returns the cache key for a processed body. When
// cache.canonical_keys is enabled the body is first reduced to a
// deterministic JSON round-trip (sorted keys, no whitespace) so that
// semantically identical requests differing only in byte layout share a
// key. Canonicalization never touches the values, so sampling
// parameters stay in the key and wrong-output risk is zero.
func (p *Pipeline) keyFor(processed []byte) string {
	if p.cfg.Get().Cache.CanonicalKeys {
		return cacheKey(cache.CanonicalJSON(processed))
	}
	return cacheKey(processed)
}

func (p *Pipeline) processInternal(ctx context.Context, req Request) (Response, error) {
	p.lastPhases = nil
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
	debugf("process request %q streaming=%v client=%q api=%v", requested, streaming, client, api)
	processed, rtkChanged := p.rtk.Apply(body)
	rtkSaved := 0
	if rtkChanged {
		rtkSaved = tokenize.Count(string(body), tokenize.KindOpenAI) - tokenize.Count(string(processed), tokenize.KindOpenAI)
		p.metrics.RTKApplied()
	}
	p.metrics.RTKSaved(int64(rtkSaved))
	if cfg := p.cfg.Get(); cfg.Cache.PrefixOrder {
		processed = orderPrompt(processed)
	}
	key := p.keyFor(processed)
	if !streaming {
		e, got, missReason := p.cache.GetWithReason(key)
		if got && !e.SSE {
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
		if got && e.SSE {
			// Entry exists but is an SSE (streaming) capture; the client
			// asked for JSON. Shape mismatch, not capacity/age.
			p.metrics.CacheMissReason("shape_mismatch")
		} else {
			p.metrics.CacheMissReason(string(missReason))
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
		runner := newRunner(p.router, p.handlers.refreshCredentials)
		result := runner.Run(ctx, cands, func(ctx context.Context, cand router.Candidate, attempt int) evalResult {
			return p.tryEval(ctx, cand, req, api, requested, body, processed, streaming, client, rtkSaved, clientFmt)
		})
		if result.OK && result.Response != nil {
			return *result.Response, nil
		}
		tryLog := result.TryLog
		allOverloaded := len(tryLog) > 0
		for _, e := range tryLog {
			if e.Class != router.ErrOverloaded.String() {
				allOverloaded = false
				break
			}
		}
		if allOverloaded {
			debugf("all overloaded for %q, retry after 1s", requested)
			time.Sleep(time.Second)
			retry := runner.Run(ctx, cands, func(ctx context.Context, cand router.Candidate, attempt int) evalResult {
				return p.tryEval(ctx, cand, req, api, requested, body, processed, streaming, client, rtkSaved, clientFmt)
			})
			if retry.OK && retry.Response != nil {
				debugf("overloaded retry success for %q", requested)
				return *retry.Response, nil
			}
			if len(retry.TryLog) > 0 {
				stillOverloaded := true
				for _, e := range retry.TryLog {
					if e.Class != router.ErrOverloaded.String() {
						stillOverloaded = false
						break
					}
				}
				if stillOverloaded {
					debugf("still overloaded for %q, second retry after 1s", requested)
					time.Sleep(time.Second)
					retry2 := runner.Run(ctx, cands, func(ctx context.Context, cand router.Candidate, attempt int) evalResult {
						return p.tryEval(ctx, cand, req, api, requested, body, processed, streaming, client, rtkSaved, clientFmt)
					})
					if retry2.OK && retry2.Response != nil {
						return *retry2.Response, nil
					}
					tryLog = retry2.TryLog
				} else {
					tryLog = retry.TryLog
				}
			} else {
				tryLog = retry.TryLog
			}
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

func (p *Pipeline) tryEval(ctx context.Context, cand router.Candidate, req Request, api apiFormat, requested string, body, processed []byte, streaming bool, client string, rtkSaved int, clientFmt apiFormat) evalResult {
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
		if clientFmt == fmtResponses {
			if wrapped, werr := dialect.OpenAIToResponses(respBody, requested); werr == nil {
				sendBody = wrapped
			}
		}
		extractor := NewExtractor()
		prompt, completion, reportedCost, cacheRead, cacheCreation := extractor.ExtractNonStreaming(respBody, body)
		p.usage.RecordFull(client, modelFromBody(body), prompt, completion, int64(rtkSaved), 0, cacheRead, cacheCreation, pricesOf(p.cfg.Get(), cand.Provider.Provider.Name), reportedCost)
		p.cache.Put(p.keyFor(processed), cacheEntry(respBody, ct, prompt, completion))
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
	if class == router.ErrAuth {
		// Auth-refresh-and-retry: runner calls refreshFn on
		// ErrAuth and re-invokes eval.
		return evalResult{Err: fmt.Errorf("provider %s: status %d (%v)", cand.Provider.Provider.Name, status, class), Class: class, Retryable: false}
	}
	if !router.IsRetryableClass(class) {
		if cand.ShouldFailoverOnClientError() {
			return evalResult{Err: fmt.Errorf("provider %s rejected model %q (HTTP %d)", cand.Provider.Provider.Name, cand.Upstream, status), Class: class, Retryable: false}
		}
		return evalResult{OK: true, Response: &Response{StatusCode: status, Body: respBody, ContentType: ct}, Class: class, Retryable: false}
	}
	p.metrics.Failure(cand.Provider.Provider.Name, class.String())
	p.router.ReportFailureWithBackoff(cand.Provider, class, retryAfter)
	return evalResult{Err: fmt.Errorf("provider %s: status %d (%v)", cand.Provider.Provider.Name, status, class), Class: class, Retryable: true}
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
