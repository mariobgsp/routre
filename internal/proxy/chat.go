package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/mariobgsp/routre/internal/cache"
	"github.com/mariobgsp/routre/internal/proxy/dialect"
	"github.com/mariobgsp/routre/internal/reqlog"
	"github.com/mariobgsp/routre/internal/tokenize"
)

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

// apiFormat is the API dialect a request arrives in.
// Deep module seam: alias to dialect.Format (see internal/proxy/dialect).
type apiFormat = dialect.Format

const (
	fmtUnknown   = dialect.FormatUnknown
	fmtOpenAI    = dialect.FormatOpenAI
	fmtAnthropic = dialect.FormatAnthropic
	fmtResponses = dialect.FormatResponses
	fmtGemini    = dialect.FormatGemini
)

// route handles one chat-style request end to end: read, compress (RTK),
// order (cache), exact-match cache lookup, tiered failover relay, cache
// write. It is shared by the /v1/chat/completions and /v1/messages handlers.
func (h *Handlers) route(w http.ResponseWriter, r *http.Request, api apiFormat) {
	start := time.Now()
	client := clientName(r)

	// Request-log + metrics emission on every exit path.
	logReq := func(e reqlog.Entry) {
		e.LatencyMS = time.Since(start).Milliseconds()
		// Per-phase observability foundation (latency survey #4).
		// Only the successful upstream attempt's phases are
		// populated; cache-served requests and pre-pipeline errors
		// leave these zero. TotalMS is the upstream-call wall
		// time (dial + headers + first body + body, depending on
		// streaming shape). Future httptrace work will split
		// this into DialMS/HeadersMS/TTFBMS.
		if h.pipeline != nil {
			if ph := h.pipeline.LastPhases(); ph != nil {
				e.DialMS = ph.DialMS
				e.HeadersMS = ph.HeadersMS
				e.TTFBMS = ph.TTFBMS
				e.TotalMS = ph.TotalMS
			}
		}
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

	// Pipeline seam: everything (streaming + non-streaming) via deep module.
	if h.pipeline != nil {
		ctx := r.Context()
		req := Request{Body: body, Path: r.URL.Path, Header: r.Header, Client: client}
		if isStreaming(body) {
			// Streaming: pipeline writes SSE directly to w and records usage.
			serr := h.pipeline.Stream(ctx, req, w)
			if serr == nil {
				logReq(reqlog.Entry{Client: client, Model: modelFromBody(body), Status: http.StatusOK, Class: "ok", Stream: true})
				return
			}
			// Stream already wrote the response — either the all-failed
			// 503 or the upstream's own 4xx surfaced verbatim. Never
			// write again; just record the real status/class so reqlog
			// and `routre logs -errors` show what the client got.
			logReq(streamOutcomeEntry(client, modelFromBody(body), serr))
			return
		} else {
			resp, perr := h.pipeline.Process(ctx, req)
			if perr == nil {
				reqModel := modelFromBody(body)
				if resp.Provider != "" {
					reqModel = resp.Provider
				}
				class := "ok"
				switch {
				case resp.FromCache:
					class = "cache"
				case resp.StatusCode >= 500:
					class = "all_failed"
				case resp.StatusCode >= 400:
					class = "error"
				}
				// Provider is the upstream that served (or tried to
				// serve) the request. Set on every non-streaming log
				// line so `routre logs -provider <name>` actually
				// filters something — previously this field was
				// always empty, which made the filter a no-op.
				logReq(reqlog.Entry{Client: client, Model: reqModel, Provider: resp.Provider, Status: resp.StatusCode, Class: class, PromptTokens: int64(tokenize.Count(string(body), tokenize.KindOpenAI))})
				for k, vv := range resp.Header {
					for _, v := range vv {
						w.Header().Add(k, v)
					}
				}
				if resp.ContentType != "" {
					w.Header().Set("Content-Type", resp.ContentType)
				}
				w.WriteHeader(resp.StatusCode)
				_, _ = w.Write(resp.Body)
				return
			}
		}
	}

	// Legacy fallback removed — pipeline now owns RTK, cache, routing,
	// translation and retry. This path is only reached if the pipeline is
	// nil (tests that construct Handlers without NewHandlers) or if both
	// pipeline.Process and pipeline.Stream failed before writing.
	// Keep a minimal honest error to avoid silent 200.
	h.Metrics.Request(client, "", modelFromBody(body), "all_failed")
	logReq(reqlog.Entry{Client: client, Model: modelFromBody(body), Status: http.StatusServiceUnavailable, Class: "all_failed"})
	w.Header().Set("Retry-After", "5")
	writeJSON(w, http.StatusServiceUnavailable, map[string]any{
		"error": map[string]any{"message": "all providers unavailable", "type": "all_providers_failed"},
	})

}

// isStreaming detects stream:true via dialect seam.
func isStreaming(body []byte) bool {
	return dialect.IsStreaming(body)
}

// streamOutcomeEntry maps a Pipeline.Stream error to its reqlog entry.
// Stream returns *streamWritten for every outcome it already put on the
// wire (all-failed 503, or the upstream's own 4xx surfaced verbatim), so
// the log carries the real status and class. Previously every terminal
// stream outcome took the success branch and was logged as
// status=200 class=ok, which is why 503 all_providers_failed responses
// never appeared in reqlog.
func streamOutcomeEntry(client, model string, err error) reqlog.Entry {
	success := reqlog.Entry{Client: client, Model: model, Status: http.StatusOK, Class: "ok", Stream: true}
	if err == nil {
		// Success: Stream returned nil. Only reachable if a caller
		// passes nil by mistake — report the success shape rather than
		// inventing a failure.
		return success
	}
	var sw *streamWritten
	if errors.As(err, &sw) {
		if sw == nil {
			// Typed-nil *streamWritten = mid-stream abort: the client
			// already has its partial 200 and the failure path wrote
			// nothing. Dereferencing sw here panicked the handler on
			// every client disconnect, so guard it explicitly.
			return success
		}
		e := reqlog.Entry{Client: client, Model: model, Status: sw.Status, Provider: sw.Provider, Stream: true}
		switch {
		case sw.Status >= 500:
			e.Class = "all_failed"
		case sw.Status >= 400:
			e.Class = "error"
		default:
			e.Class = "ok"
		}
		return e
	}
	// Unknown pre-write failure (unreachable today — Stream only returns
	// nil or *streamWritten): keep the conservative all-failed shape.
	return reqlog.Entry{Client: client, Model: model, Status: http.StatusServiceUnavailable, Class: "all_failed", Stream: true}
}

// cacheKey is the exact-match key over the processed body (post-RTK,
// post-ordering). The "stream" flag is stripped so the same prompt
// hits whether the client used stream:true or stream:false. The body
// is canonicalized (deterministic JSON round-trip) when canonical
// keys are enabled, which the pipeline does before calling this.
func cacheKey(processed []byte) string {
	if bytes.Contains(processed, []byte(`"stream"`)) {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(processed, &m); err == nil {
			if _, ok := m["stream"]; ok {
				delete(m, "stream")
				if b, err := json.Marshal(m); err == nil {
					return cache.Key(b)
				}
			}
		}
	}
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

// writeStatus copies an upstream error response to the client.
func writeStatus(w http.ResponseWriter, status int, body []byte, ct string) {
	if ct == "" {
		ct = "application/json"
	}
	w.Header().Set("Content-Type", ct)
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
