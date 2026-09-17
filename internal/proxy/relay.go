package proxy

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mariobgsp/routre/internal/proxy/dialect"
	"github.com/mariobgsp/routre/internal/router"
)

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

// firstByteBody is a first-byte signal wrapper: the relay's first
// successful Read closes `firstByte`, cancelling the firstByteTimeout
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
	// Opencode session header: required by opencode.ai/zen from 09/06.
	// Forward client's x-opencode-session if present, else inject a stable
	// fallback so Go HTTP client / curl user-agents never error.
	// ponytail: stable per-gateway instance, not per-request random, so a
	// stable per-conversation ID without tracking conversations. Upgrade to
	// per-client/per-conversation map if opencode optimizes on it.
	if v := r.Header.Get("x-opencode-session"); v != "" {
		req.Header.Set("x-opencode-session", v)
	} else if isNativeResponses(baseURL) {
		req.Header.Set("x-opencode-session", opencodeSessionID())
	}
	return req, nil
}

// relay performs the actual upstream call (non-streaming returns the body;
// streaming relays SSE to w). from is the client's dialect for cross-kind
// translation; retryAfter is the parsed upstream Retry-After (0 when absent).
func (h *Handlers) relay(ctx context.Context, w http.ResponseWriter, baseURL string, r *http.Request, payload []byte, streaming bool, kind, apiKeyEnv string, from apiFormat, clientFmt apiFormat) (int, []byte, string, time.Duration, streamUsage, error) {
	if streaming {
		return h.relayStream(ctx, w, baseURL, r, payload, kind, apiKeyEnv, clientFmt)
	}
	path := "/v1/chat/completions"
	if clientFmt == fmtResponses && isNativeResponses(baseURL) {
		path = "/v1/responses"
	} else if kind == "anthropic" {
		path = "/v1/messages"
	} else if kind == "gemini" {
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
	if from == fmtResponses && isNativeResponses(baseURL) {
		path = "/v1/responses"
	} else if kind == "anthropic" {
		path = "/v1/messages"
	} else if kind == "gemini" {
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
		if h.Logger != nil {
			h.Logger.Printf("[DEBUG upstream] %s%s status=%d body=%.500s payload=%.500s", baseURL, path, resp.StatusCode, string(body), string(payload))
		}
		return resp.StatusCode, body, resp.Header.Get("Content-Type"), parseRetryAfter(resp.Header.Get("Retry-After")), streamUsage{}, nil
	}

	to := apiFormat(dialect.KindToFormat(kind))
	if from == fmtResponses && isNativeResponses(baseURL) {
		to = fmtResponses
	}
	susage, serr := h.streamRelay(w, resp, from, to)
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
	// Commit the status lazily, on the first body byte, instead of eagerly:
	// a 2xx upstream stream that dies before its first byte is documented as
	// retryable/failover-able, and only genuinely is while no header has
	// reached the client. Committing 200 up front made the final all-failed
	// 503 a superfluous WriteHeader over an already-committed 200.
	cap.pending = resp.StatusCode

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
	buf     []byte
	max     int
	failed  bool
	pending int // status to commit on first write/flush (0 = none)
}

// commit sends the deferred WriteHeader exactly once, before the first body
// byte (or flush) reaches the client.
func (c *captureWriter) commit() {
	if c.pending != 0 {
		c.ResponseWriter.WriteHeader(c.pending)
		c.pending = 0
	}
}

func (c *captureWriter) Write(p []byte) (int, error) {
	c.commit()
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
	c.commit()
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
