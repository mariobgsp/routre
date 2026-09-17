package proxy

import (
	"bytes"
	"context"
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

func (p *Pipeline) processInternal(ctx context.Context, req Request) (Response, error) {
	p.lastPhases = nil
	body := req.Body
	path := req.Path
	client := req.Client
	api := apiFormat(dialect.DetectFormat(path, body))
	clientFmt := api
	// ponytail: same as Stream — keep Responses native, translate per-candidate
	streaming := dialect.IsStreaming(body)
	sanitizedBody := body
	if clientFmt == fmtResponses {
		sanitizedBody = sanitizeResponsesPayload(body)
	}
	requested := modelFromBody(sanitizedBody)
	debugf("process request %q streaming=%v client=%q api=%v", requested, streaming, client, api)
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
	key := p.keyFor(processed)
	if !streaming && cacheableRequest(clientFmt, processed) {
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
			if clientFmt == fmtResponses && !bytes.Contains(e.Body, []byte(`"object":"response"`)) {
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
		// bad for this model), not a 503. If every candidate hit a
		// billing rejection, the user should see a terminal 402
		// (credits exhausted) rather than a retryable-looking 503 that
		// makes clients retry the same losing round. Only the mixed /
		// server-side case stays as 503.
		allClient, allAuth, allCredits := true, true, true
		for _, e := range tryLog {
			if e.Class != router.ErrClient.String() {
				allClient = false
			}
			if e.Class != router.ErrAuth.String() {
				allAuth = false
			}
			if e.Class != router.ErrCredits.String() {
				allCredits = false
			}
		}
		switch {
		case allClient && len(tryLog) > 0:
			// Every provider rejected the model (400/404/422). This
			// is a model-not-found, not a service outage.
			p.metrics.Request(client, "*", requested, "client")
			body, _ := failures.RenderBody(failures.KindModelNotFound, requested, tryLog, 0)
			return Response{StatusCode: 404, Body: body, Header: http.Header{"Content-Type": []string{"application/json"}}, Provider: tryLog[0].Provider}, nil
		case allAuth && len(tryLog) > 0:
			// Every provider returned 401/403: the configured keys
			// don't authorize this model. Surface as 502 (bad
			// gateway) so the user knows the gateway is fine, the
			// auth is wrong.
			p.metrics.Request(client, "*", requested, "auth")
			body := []byte(fmt.Sprintf(`{"error":{"message":"all configured providers rejected the request (auth). check API keys and model access.","type":"all_providers_unauthorized","model":%q,"attempts":%s}}`,
				requested, mustJSON(tryLog)))
			return Response{StatusCode: 502, Body: body, Header: http.Header{"Content-Type": []string{"application/json"}}, Provider: tryLog[0].Provider}, nil
		case allCredits && len(tryLog) > 0:
			// Every provider reported a billing rejection (401
			// CreditsError / 402): the balance is exhausted on all of
			// them. 402 is terminal — the client must top up or pick
			// another account, not retry.
			p.metrics.Request(client, "*", requested, "credits")
			body := []byte(fmt.Sprintf(`{"error":{"message":"all configured providers rejected the request (insufficient credits). top up the account or use a model the provider serves for free.","type":"all_providers_insufficient_credits","model":%q,"attempts":%s}}`,
				requested, mustJSON(tryLog)))
			return Response{StatusCode: 402, Body: body, Header: http.Header{"Content-Type": []string{"application/json"}}, Provider: tryLog[0].Provider}, nil
		}
		// Mixed classes (some 5xx, some 4xx, some auth) — genuine
		// outage. Surface as 503 with the per-provider breakdown. An
		// empty breakdown is impossible here (every terminal outcome
		// records an attempt); if it ever happens, report the internal
		// bug instead of an unactionable "all providers failed".
		if len(tryLog) == 0 {
			debugf("all-failed with empty tryLog for %q — no upstream attempt recorded", requested)
			return Response{StatusCode: http.StatusBadGateway, Body: emptyAttemptsBody(requested), ContentType: "application/json"}, nil
		}
		prov := ""
		if len(tryLog) > 0 {
			prov = tryLog[0].Provider
		}
		finalOverloaded := len(tryLog) > 0
		for _, e := range tryLog {
			if e.Class != router.ErrOverloaded.String() {
				finalOverloaded = false
				break
			}
		}
		retryAfter := 5 * time.Second
		if finalOverloaded {
			retryAfter = time.Second
		}
		p.metrics.Request(client, "*", requested, "all_failed")
		body, hdr := failures.RenderBody(failures.KindAllFailed, requested, tryLog, retryAfter)
		return Response{StatusCode: 503, Body: body, Header: hdr, Provider: prov}, nil
	}
	return Response{}, fmt.Errorf("streaming request: use Stream")
}
