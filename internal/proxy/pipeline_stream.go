package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/mariobgsp/routre/internal/cache"
	"github.com/mariobgsp/routre/internal/proxy/dialect"
	"github.com/mariobgsp/routre/internal/proxy/failures"
	"github.com/mariobgsp/routre/internal/router"
	"github.com/mariobgsp/routre/internal/tokenize"
	"github.com/mariobgsp/routre/internal/usage"
)

// streamWritten marks a Stream outcome already on the wire (upstream 4xx
// surfaced verbatim, or the all-failed render). Never write again — record
// Status/Provider to reqlog/metrics instead.
type streamWritten struct {
	Status   int
	Provider string
}

func (e *streamWritten) Error() string {
	return fmt.Sprintf("stream response already written: status %d", e.Status)
}

// emptyAttemptsBody is the all-failed body for the unreachable "no attempt
// recorded" state — an eval swallowed a failure, i.e. a routre bug. Named
// as internal_error so upstreams are never blamed for it.
func emptyAttemptsBody(model string) []byte {
	body, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": fmt.Sprintf("routre recorded no upstream attempt for model %q (all-failed state is a routre bug, please report it)", model),
			"type":    "internal_error",
			"model":   model,
		},
	})
	if err != nil {
		// Cannot happen for strings; keep the wire shape valid if it ever does.
		return []byte(`{"error":{"message":"routre internal error: no upstream attempt recorded","type":"internal_error"}}`)
	}
	return body
}

// writeUpstreamError surfaces a deterministic upstream failure verbatim.
// Rendering "all providers failed" instead would hide an actionable client
// error (bad prompt, bad max_tokens) behind a 503.
func writeUpstreamError(w http.ResponseWriter, status int, ct string, body []byte, model string) {
	if status < 400 {
		status = http.StatusBadGateway
	}
	if len(body) == 0 {
		body = []byte(fmt.Sprintf(`{"error":{"message":"upstream returned HTTP %d","type":"upstream_error","model":%q}}`, status, model))
	}
	writeStatus(w, status, body, ct)
}

// streamFinished reports whether a runner round already produced the
// terminal response (handled=true), and the error Stream returns for it.
// Every runnerResult consumer must go through this, including retry rounds:
// a retry that committed a 4xx must not fall through to the all-failed
// render (second WriteHeader + wrong reqlog status).
func streamFinished(res runnerResult) (err error, handled bool) {
	switch {
	case res.OK:
		return nil, true
	case res.Emitted:
		if res.Written == nil {
			// Mid-stream abort: the client already has a partial 200.
			return nil, true
		}
		return res.Written, true
	default:
		return nil, false
	}
}

func (p *Pipeline) Stream(ctx context.Context, req Request, w http.ResponseWriter) error {
	p.lastPhases = nil
	body := req.Body
	path := req.Path
	client := req.Client
	header := req.Header
	api := apiFormat(dialect.DetectFormat(path, body))
	clientFmt := api
	debugf("stream request %q client=%q api=%v", modelFromBody(body), client, api)
	// Keep Responses payloads native here (muse-spark serves only
	// /v1/responses); translation to chat.completions is per-candidate.
	// Sanitize-then-key so replays share the safe-prefix cache key.
	sanitizedBody := body
	if clientFmt == fmtResponses {
		sanitizedBody = sanitizeResponsesPayload(body)
	}
	requested := modelFromBody(sanitizedBody)
	processed, rtkChanged := p.rtk.Apply(sanitizedBody)
	rtkSaved := 0
	if rtkChanged {
		rtkSaved = tokenize.Count(string(sanitizedBody), tokenize.KindOpenAI) - tokenize.Count(string(processed), tokenize.KindOpenAI)
		p.metrics.RTKApplied()
	}
	p.metrics.RTKSaved(int64(rtkSaved))
	if cfg := p.cfg.Get(); cfg.Cache.PrefixOrder {
		processed = orderPrompt(processed)
	}
	// Streaming replay cache: byte-identical SSE captures stay
	// self-consistent (tool ids, finish_reason, [DONE]) by construction.
	if cacheableRequest(clientFmt, processed) {
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
	}
	cands := p.router.CandidatesWithFallbacks(requested, p.cfg.Get().Fallbacks)
	if len(cands) == 0 {
		// No candidate was even eligible (model unlisted or every serving
		// provider is in cooldown). Surface the reason on the wire so the
		// user can see it without a separate status call.
		if retryAfter, served := p.router.MinCooldownForModel(requested, p.cfg.Get().ForwardUnknown); served {
			failures.Render(w, failures.KindProvidersUnavailable, requested,
				[]failures.Outcome{{Provider: "*", Cooldown: retryAfter}}, retryAfter)
			// Report the status that reached the wire: returning nil here
			// made route log a client-visible 503 as status=200 class="ok",
			// so `routre logs -errors` never showed it.
			return &streamWritten{Status: http.StatusServiceUnavailable}
		}
		failures.Render(w, failures.KindModelNotFound, requested, nil, 0)
		return &streamWritten{Status: http.StatusServiceUnavailable}
	}
	// Per-cand retry + auth refresh + tryLog accumulation now flow
	// through candidateRunner (internal/proxy/runner.go). The eval
	// closure below is the per-attempt work; the runner owns the
	// iteration policy.
	runner := newRunner(p.router, p.handlers.refreshCredentials)
	result := runner.Run(ctx, cands, func(ctx context.Context, cand router.Candidate, attempt int) evalResult {
		return p.streamEval(ctx, cand, attempt, w, header, api, requested, body, processed, client, rtkSaved, clientFmt)
	})
	// Terminal check: a candidate succeeded (bytes written), the round
	// committed a deterministic 4xx verbatim, or the stream aborted
	// mid-flight (partial 200 — report a true nil, never a typed-nil
	// *streamWritten, or callers' `err != nil` checks fire).
	if err, done := streamFinished(result); done {
		return err
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
		// The retry round can itself commit a terminal response (a
		// deterministic 4xx surfaced verbatim, or a mid-stream abort).
		// It must be checked here — before the reassignment below — or the
		// all-failed render writes a second status over the committed one.
		if err, done := streamFinished(retry); done {
			return err
		}
		result = retry
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
				if err, done := streamFinished(retry2); done {
					return err
				}
				result = retry2
			}
		}
	}
	if len(result.TryLog) == 0 {
		// No attempt recorded and nothing committed: safe to write the
		// honest internal-error body (empty attempts[] would blame upstreams
		// for a routre bug).
		debugf("all-failed with empty tryLog for %q — no upstream attempt recorded", requested)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write(emptyAttemptsBody(requested))
		return &streamWritten{Status: http.StatusBadGateway}
	}
	retryAfter := 5 * time.Second
	if allOverloaded {
		retryAfter = time.Second
	}
	failures.Render(w, failures.KindAllFailed, requested, result.TryLog, retryAfter)
	return &streamWritten{Status: http.StatusServiceUnavailable}
}

// streamEval is the per-attempt streaming eval: prep + relay, then report
// stop (OK) / retry (Retryable) / next-candidate to the runner.
func (p *Pipeline) streamEval(ctx context.Context, cand router.Candidate, _ int, w http.ResponseWriter, header http.Header, api apiFormat, requested string, body, processed []byte, client string, rtkSaved int, clientFmt apiFormat) evalResult {
	payload, perr := p.preparePayload(api, clientFmt, cand, requested, processed)
	if perr != nil {
		p.router.ReportFailure(cand.Provider, router.ErrClient)
		return evalResult{Err: perr, Class: router.ErrClient, Retryable: false}
	}
	kind := cand.Provider.Provider.Kind
	dummyReq := &http.Request{Header: header}
	streamCtx, cancel := context.WithCancel(ctx) // streams run unbounded
	defer cancel()
	relayStart := time.Now()
	status, errBody, ct, retryAfter, susage, rerr := p.handlers.relay(streamCtx, w, cand.Provider.Provider.BaseURL, dummyReq, payload, true, kind, cand.Provider.Provider.APIKeyEnv, api, clientFmt)
	// One sanitized retry for providers that renamed a caller-bound field.
	if rerr == nil && clientFmt == fmtResponses && isReasoningStateError(status, errBody) {
		if sanitized := sanitizeResponsesPayload(processed); string(sanitized) != string(processed) {
			if sp, serr := p.preparePayload(api, clientFmt, cand, requested, sanitized); serr == nil {
				debugf("reasoning-state retry for %q", requested)
				status, errBody, ct, retryAfter, susage, rerr = p.handlers.relay(streamCtx, w, cand.Provider.Provider.BaseURL, dummyReq, sp, true, kind, cand.Provider.Provider.APIKeyEnv, api, clientFmt)
			}
		}
	}
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
			// Defensive: Classify yields only retryable classes here, but a
			// future class must surface explicitly, never as an empty 200.
			writeStatus(w, http.StatusBadGateway,
				[]byte(fmt.Sprintf(`{"error":{"message":%q,"type":"upstream_error","model":%q}}`, rerr.Error(), requested)),
				"application/json")
			return evalResult{
				OK: true, Err: rerr, Class: class, Retryable: false, Emitted: true,
				Written: &streamWritten{Status: http.StatusBadGateway, Provider: cand.Provider.Provider.Name},
			}
		}
		p.router.ReportFailureWithBackoff(cand.Provider, class, retryAfter)
		return evalResult{Err: rerr, Class: class, Retryable: true}
	}
	if status >= 200 && status < 300 {
		p.router.ReportSuccess(cand.Provider)
		// Usage comes from the in-relay SSE sniffer, never buffered.
		prompt := susage.prompt
		if prompt == 0 {
			prompt = int64(tokenize.Count(string(processed), tokenize.KindOpenAI))
		}
		p.usage.RecordFull(client, modelFromBody(body), prompt, susage.completion, int64(rtkSaved), 0, susage.cacheRead, susage.cacheCreation, pricesOf(p.cfg.Get(), cand.Provider.Provider.Name), 0)
		p.metrics.CacheRead(cand.Provider.Provider.Name, susage.cacheRead)
		p.metrics.CacheCreation(cand.Provider.Provider.Name, susage.cacheCreation)
		p.metrics.Request(client, cand.Provider.Provider.Name, requested, "ok")
		// Streaming replay cache: safe-prefix only, never caller-bound state.
		if cacheableRequest(clientFmt, processed) && len(susage.captured) > 0 {
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
		return evalResult{Err: fmt.Errorf("provider %s: status %d (%v)", cand.Provider.Provider.Name, status, class), Class: class, Retryable: false}
	}
	if class == router.ErrCredits {
		// A billing rejection is deterministic: the balance does not
		// heal in 500ms, so a same-cand retry just burns a second full
		// wall-clock attempt. Fail over to the next candidate once.
		// ReportFailure is a cooldown no-op for ErrCredits (the
		// provider may still serve free variants).
		p.metrics.Failure(cand.Provider.Provider.Name, class.String())
		p.router.ReportFailureWithBackoff(cand.Provider, class, retryAfter)
		return evalResult{Err: fmt.Errorf("provider %s: status %d (%v)", cand.Provider.Provider.Name, status, class), Class: class, Retryable: false}
	}
	if !router.IsRetryableClass(class) {
		if cand.ShouldFailoverOnClientError() {
			return evalResult{Err: fmt.Errorf("provider %s rejected model %q (HTTP %d)", cand.Provider.Provider.Name, cand.Upstream, status), Class: class, Retryable: false}
		}
		// Deterministic rejection from the listed provider for this
		// model (over-long prompt, out-of-range max_tokens, unsupported
		// parameter): no other candidate can serve it either. Nothing
		// has been committed to the stream yet — the relay commits its
		// status lazily on the first body byte — so surface the
		// upstream's own status and body. Rendering the all-failed 503
		// here is what turned commandcode's "maximum context length is
		// 1048576 tokens" 400 into an unactionable
		// "all providers failed" with an empty attempts[] array.
		writeUpstreamError(w, status, ct, errBody, requested)
		p.metrics.Failure(cand.Provider.Provider.Name, class.String())
		return evalResult{
			OK:        true,
			Err:       fmt.Errorf("provider %s: status %d (%v)", cand.Provider.Provider.Name, status, class),
			Class:     class,
			Retryable: false,
			Emitted:   true,
			Written:   &streamWritten{Status: status, Provider: cand.Provider.Provider.Name},
		}
	}
	p.metrics.Failure(cand.Provider.Provider.Name, class.String())
	p.router.ReportFailureWithBackoff(cand.Provider, class, retryAfter)
	return evalResult{Err: fmt.Errorf("provider %s: status %d (%v)", cand.Provider.Provider.Name, status, class), Class: class, Retryable: true}
}

// preparePayload produces the wire body for one candidate: cross-kind
// translate, model rewrite, max_tokens clamp, and the Anthropic
// prompt-cache injection. Pulled out of the per-attempt evals so
// streaming and non-streaming share the same per-cand prep.
// isNativeResponses reports whether an upstream speaks /v1/responses natively.
