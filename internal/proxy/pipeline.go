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
	// Phases is this request's own per-attempt timing. Never shared: the
	// pipeline serves concurrent requests from one instance.
	Phases *Phases
}

func (p *Pipeline) Process(ctx context.Context, req Request) (Response, error) {
	return p.processInternal(ctx, req)
}

func (p *Pipeline) processInternal(ctx context.Context, req Request) (Response, error) {
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
	env := newEnvelope(sanitizedBody)
	requested := env.requested
	debugf("process request %q streaming=%v client=%q api=%v", requested, streaming, client, api)
	env.applyRTK(p.rtk)
	if env.rtkChanged {
		p.metrics.RTKApplied()
	}
	p.metrics.RTKSaved(int64(env.rtkSaved))
	if cfg := p.cfg.Get(); cfg.Cache.PrefixOrder {
		env.orderPrompt()
	}
	env.finish(p.cfg.Get().Cache.CanonicalKeys)
	processed := env.body
	if !streaming && cacheableRequest(clientFmt, processed) {
		e, got, missReason := p.cache.GetWithReason(env.key)
		if got && !e.SSE {
			cacheSaved := e.PromptTokens
			if cacheSaved == 0 {
				cacheSaved = tokenize.CountCapped(string(processed))
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
		result := runner.Run(ctx, cands, func(ctx context.Context, cand router.Candidate, attempt int, budget time.Duration) evalResult {
			return p.tryEval(ctx, cand, req, api, requested, body, env, streaming, client, clientFmt, budget)
		})
		if result.OK && result.Response != nil {
			r := *result.Response
			r.Phases = result.Phases
			return r, nil
		}
		tryLog := result.TryLog
		// An all-overloaded round renders immediately with Retry-After: 1 (see
		// the render path below) — no sleep and no extra candidate round.
		if result.BudgetExhausted {
			tryLog = append(tryLog, failures.Outcome{
				Provider: "*",
				Class:    "failover_budget",
				Err:      fmt.Sprintf("failover budget %s exhausted before trying %d candidate(s)", requestFailoverBudget, result.Untried),
			})
		}
		// Uniform failures get honest statuses: all-4xx → 404 (unknown
		// model, not an outage), all-auth → 502 (bad keys), all-billing
		// → 402 (top up, don't retry). Mixed/server-side stays 503.
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
			p.metrics.Request(client, "*", requested, "client")
			body, _ := failures.RenderBody(failures.KindModelNotFound, requested, tryLog, 0)
			return Response{StatusCode: 404, Body: body, Header: http.Header{"Content-Type": []string{"application/json"}}, Provider: tryLog[0].Provider}, nil
		case allAuth && len(tryLog) > 0:
			p.metrics.Request(client, "*", requested, "auth")
			body := []byte(fmt.Sprintf(`{"error":{"message":"all configured providers rejected the request (auth). check API keys and model access.","type":"all_providers_unauthorized","model":%q,"attempts":%s}}`,
				requested, mustJSON(tryLog)))
			return Response{StatusCode: 502, Body: body, Header: http.Header{"Content-Type": []string{"application/json"}}, Provider: tryLog[0].Provider}, nil
		case allCredits && len(tryLog) > 0:
			p.metrics.Request(client, "*", requested, "credits")
			body := []byte(fmt.Sprintf(`{"error":{"message":"all configured providers rejected the request (insufficient credits). top up the account or use a model the provider serves for free.","type":"all_providers_insufficient_credits","model":%q,"attempts":%s}}`,
				requested, mustJSON(tryLog)))
			return Response{StatusCode: 402, Body: body, Header: http.Header{"Content-Type": []string{"application/json"}}, Provider: tryLog[0].Provider}, nil
		}
		// Mixed failure: genuine 503 with the per-provider breakdown.
		// tryLog is non-empty here (empty returns above), so index directly.
		if len(tryLog) == 0 {
			debugf("all-failed with empty tryLog for %q — no upstream attempt recorded", requested)
			return Response{StatusCode: http.StatusBadGateway, Body: emptyAttemptsBody(requested), ContentType: "application/json"}, nil
		}
		prov := tryLog[0].Provider
		finalOverloaded := true
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
