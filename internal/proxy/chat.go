package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mariobgsp/routre/internal/cache"
	"github.com/mariobgsp/routre/internal/config"
	"github.com/mariobgsp/routre/internal/proxy/dialect"
	"github.com/mariobgsp/routre/internal/reqlog"
	"github.com/mariobgsp/routre/internal/router"
	"github.com/mariobgsp/routre/internal/tokenize"
)

// attemptTimeout bounds a single non-streaming upstream attempt. Streaming
// relays are exempt (they can legitimately run for minutes; the transport
// already bounds dial + response headers).
const attemptTimeout = 30 * time.Second

// firstByteTimeout caps the wait for the FIRST upstream body byte in a
// streaming response. ResponseHeaderTimeout (20s) covers the headers;
// this covers the gap between headers and the first token. After the
// first byte the relay runs unbounded (a long stream is expected). A
// slow first byte usually means the provider is stuck (auth, model
// warm-up) and we should fail over rather than hang the client.
//
// ponytail: 30s is a generous tail-killer. Tighten to 10s once a
// per-phase histogram (latency survey #13) shows the real p99.
const firstByteTimeout = 30 * time.Second

// firstByteBody wraps a streaming response body so the relay's first
// successful Read signals `firstByte`, cancelling the firstByteTimeout
// timer in relayStream. After the first byte, reads pass through
// unchanged; the relay runs unbounded for the rest of the stream. If
// the timer fires first, relayStream closes the body, in-flight Reads
// return an error, and the relay reports a pre-first-byte failure
// (which the runner treats as retryable → failover).
type firstByteBody struct {
	io.ReadCloser
	firstByte chan struct{}
	once      sync.Once
}

func (b *firstByteBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.once.Do(func() { close(b.firstByte) })
	}
	return n, err
}

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

// detectFormat guesses the dialect from the request path and body shape.
// Delegates to dialect seam.
func detectFormat(path string, body []byte) apiFormat {
	return apiFormat(dialect.DetectFormat(path, body))
}

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
			// Pipeline failed before any byte reached the client: it already
			// wrote a 503 body to w. Log the request so reqlog shows the
			// failure (previously the streaming 503 path was silent here).
			logReq(reqlog.Entry{Client: client, Model: modelFromBody(body), Status: http.StatusServiceUnavailable, Class: "all_failed", Stream: true})
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

// cacheKey is the exact-match key over the processed body (post-RTK,
// post-ordering). The "stream" flag is stripped so the same prompt
// hits whether the client used stream:true or stream:false.
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

// kindOf maps a provider kind string to an apiFormat via dialect.
func kindOf(kind string) apiFormat {
	return apiFormat(dialect.KindToFormat(kind))
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
		return nil, fmt.Errorf("provider key %s is not set (use `routre setup` or export it)", apiKeyEnv)
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

	// Bound the wait for the first upstream body byte. The relay
	// signals success via firstByteOK.Close() on its first Write; if
	// the timer fires first, the body is closed, in-flight Reads
	// error, and the relay reports a pre-first-byte failure
	// (retryable → failover). Stops a stuck provider from hanging
	// the client for minutes.
	firstByteOK := make(chan struct{})
	firstByteTimer := time.AfterFunc(firstByteTimeout, func() {
		select {
		case <-firstByteOK:
			// Already succeeded; no-op.
		default:
			resp.Body.Close()
		}
	})
	defer firstByteTimer.Stop()
	resp.Body = &firstByteBody{ReadCloser: resp.Body, firstByte: firstByteOK}

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
	// Capture client-dialect bytes for the streaming replay cache while
	// relaying. The wrapper tees every successful Write; on any write
	// failure (client went away) capture is abandoned so a truncated stream
	// can never be cached.
	cap := &captureWriter{ResponseWriter: w, max: 8 << 20}
	w = cap

	// Copy headers from upstream.
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set("X-Llrouter-Streaming", "true")
	w.WriteHeader(resp.StatusCode)

	flusher, _ := w.(http.Flusher)

	// Cross-kind: via dialect seam (SSE state-machine translator).
	sniffer := newUsageSniffer(resp.Body)
	if from != to {
		err := dialect.New().Stream(dialect.Format(from), dialect.Format(to), sniffer, w, func() {
			if flusher != nil {
				flusher.Flush()
			}
		})
		sniffer.drainCarry()
		if err != nil {
			// Failed before any byte reached the client: retryable.
			// ErrAborted means bytes already reached the client: surface as
			// the gateway's stream-abort contract (no failover, no caching).
			if errors.Is(err, dialect.ErrAborted) {
				return streamUsage{}, router.StreamAborted()
			}
			return streamUsage{}, err
		}
		return sniffer.usage().withCaptured(cap), nil
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
		return sniffer.usage().withCaptured(cap), nil
	}

	buf := make([]byte, 32*1024)
	firstByte := true
	for {
		n, rerr := sniffer.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				// Client went away mid-stream: not an upstream failure.
				return streamUsage{}, nil
			}
			if firstByte {
				firstByte = false
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				sniffer.drainCarry()
				return sniffer.usage().withCaptured(cap), nil
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

// captureWriter tees bytes written to the client into a bounded buffer for
// the streaming replay cache. Implements http.Flusher unconditionally so the
// relay's flusher lookup keeps working through the wrapper.
type captureWriter struct {
	http.ResponseWriter
	buf    []byte
	max    int
	failed bool
}

func (c *captureWriter) Write(p []byte) (int, error) {
	n, err := c.ResponseWriter.Write(p)
	if err != nil {
		c.failed = true // client gone: never cache a truncated stream
		return n, err
	}
	if !c.failed && len(c.buf) < c.max {
		room := c.max - len(c.buf)
		if len(p) > room {
			p = p[:room]
		}
		c.buf = append(c.buf, p...)
	}
	return n, nil
}

func (c *captureWriter) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// withCaptured attaches the captured bytes to a successful stream usage.
func (u streamUsage) withCaptured(c *captureWriter) streamUsage {
	if !c.failed && len(c.buf) > 0 {
		u.captured = c.buf
	}
	return u
}

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
			// Upstream died mid-stream after bytes were relayed: not
			// retryable (client has partial data), but must NOT be treated
			// as a clean end — otherwise the truncated stream would be
			// cached as a replay entry.
			return router.StreamAborted()
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

// refreshCredentials re-reads the routre.env key file and reports
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
