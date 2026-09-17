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

// firstByteTimeout caps the wait for the FIRST upstream body byte; after it
// the relay runs unbounded. A slow first byte means a stuck provider —
// fail over instead of hanging the client.
// ponytail: generous tail-killer; tighten to 10s once latency data shows p99.
const firstByteTimeout = 30 * time.Second

// firstByteBody closes firstByte on its first successful Read (pre-first-byte
// failures are retryable → failover).
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

// buildUpstreamRequest prepares the upstream request (key + passthrough
// headers) without sending it. streaming defaults Accept to event-stream.
func (h *Handlers) buildUpstreamRequest(ctx context.Context, baseURL, kind, path string, payload []byte, r *http.Request, apiKeyEnv string, streaming bool) (*http.Request, error) {
	base := strings.TrimRight(baseURL, "/")
	if strings.HasSuffix(base, "/v1") {
		path = strings.TrimPrefix(path, "/v1") // base already includes /v1
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if streaming {
		if accept := r.Header.Get("Accept"); accept == "" {
			req.Header.Set("Accept", "text/event-stream")
		} else {
			req.Header.Set("Accept", accept)
		}
	}
	// The gateway holds the provider key, never the client: a client-sent
	// Authorization header is a placeholder and must not reach upstream.
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
// upstreamPath selects the provider endpoint. fmt is the client's dialect for
// the non-streaming path (clientFmt) and the source dialect for streams
// (from); Responses stays native only on Responses-capable upstreams.
func upstreamPath(fmt apiFormat, kind, baseURL string, payload []byte, stream bool) string {
	if fmt == fmtResponses && isNativeResponses(baseURL) {
		return "/v1/responses"
	}
	switch kind {
	case "anthropic":
		return "/v1/messages"
	case "gemini":
		p := "/v1beta/models/" + modelFromBody(payload) + ":generateContent"
		if stream {
			p += "?alt=sse"
		}
		return p
	default:
		return "/v1/chat/completions"
	}
}

func (h *Handlers) relay(ctx context.Context, w http.ResponseWriter, baseURL string, r *http.Request, payload []byte, streaming bool, kind, apiKeyEnv string, from apiFormat, clientFmt apiFormat) (int, []byte, string, time.Duration, streamUsage, error) {
	if streaming {
		return h.relayStream(ctx, w, baseURL, r, payload, kind, apiKeyEnv, clientFmt)
	}
	path := upstreamPath(clientFmt, kind, baseURL, payload, false)
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

// relayStream streams an SSE response to the client. Non-2xx upstreams are
// returned (not streamed) so the caller can classify and fail over.
// Pre-first-byte errors are retryable; post-first-byte is StreamAborted.
func (h *Handlers) relayStream(ctx context.Context, w http.ResponseWriter, baseURL string, r *http.Request, payload []byte, kind, apiKeyEnv string, from apiFormat) (int, []byte, string, time.Duration, streamUsage, error) {
	path := upstreamPath(from, kind, baseURL, payload, true)
	req, err := h.buildUpstreamRequest(ctx, baseURL, kind, path, payload, r, apiKeyEnv, true)
	if err != nil {
		return 0, nil, "", 0, streamUsage{}, err
	}

	resp, err := h.HTTPClient.Do(req)
	if err != nil {
		return 0, nil, "", 0, streamUsage{}, err
	}
	defer resp.Body.Close()

	// Fail pre-first-byte (retryable → failover) if the upstream stalls.
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

	// A non-2xx response is NOT a stream: return it for classify + failover.
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

// streamRelay copies an SSE stream to the client (translating across dialects)
// and returns the usage captured from it.
func (h *Handlers) streamRelay(w http.ResponseWriter, resp *http.Response, from, to apiFormat) (streamUsage, error) {
	// Tee client-dialect bytes for the replay cache; a failed capture
	// (client went away) is abandoned so truncated streams never cache.
	cap := &captureWriter{ResponseWriter: w, max: 8 << 20}
	w = cap

	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set("X-Llrouter-Streaming", "true")
	// Lazy status commit: a pre-first-byte death stays retryable only while
	// no header reached the client.
	cap.pending = resp.StatusCode

	flusher, _ := w.(http.Flusher)

	// Cross-kind translation via the dialect seam.
	sniffer := newUsageSniffer(resp.Body)
	if from != to {
		err := dialect.New().Stream(dialect.Format(from), dialect.Format(to), sniffer, w, func() {
			if flusher != nil {
				flusher.Flush()
			}
		})
		sniffer.drainCarry()
		if err != nil {
			// Pre-first-byte failure: retryable. ErrAborted (bytes already
			// sent): stream-abort contract, no failover, no caching.
			if errors.Is(err, dialect.ErrAborted) {
				return streamUsage{}, router.StreamAborted()
			}
			return streamUsage{}, err
		}
		return sniffer.usage().withCaptured(cap), nil
	}

	// Same-kind OpenAI: guarantee a terminal finish_reason chunk (some
	// providers close the stream without one; strict clients error).
	// Other dialects keep the raw byte-copy path below.
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

// syntheticFinishChunk is the terminal finish_reason chunk injected when the
// upstream ends the stream without sending one.
var syntheticFinishChunk = []byte(`data: {"object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n")

// captureWriter tees client-bound bytes into a bounded buffer for the replay
// cache. Always implements http.Flusher so the relay keeps flushing.
type captureWriter struct {
	http.ResponseWriter
	buf     []byte
	max     int
	failed  bool
	pending int // status to commit on first write/flush (0 = none)
}

// commit sends the deferred WriteHeader once, before the first body byte.
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

// relayOpenAIGuaranteeFinish relays SSE frames, injecting a finish_reason
// chunk when the upstream omits one (pass-through otherwise).
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
			// Mid-stream death after bytes were relayed: not retryable, and
			// never a clean end (a truncated stream must not cache).
			return router.StreamAborted()
		}
	}
}

// readRawFrame reads one raw SSE frame (through the blank line or EOF).
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
