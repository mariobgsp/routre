// Package mock provides an in-process mock upstream provider for tests and
// for the RAM/benchmark harness. It supports:
//   - deterministic SSE streaming;
//   - failure injection (status codes, connection reset, mid-stream abort);
//   - request counting, so tests can assert failover order.
package mock

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Server is a configurable mock upstream.
type Server struct {
	mu        sync.Mutex
	ln        net.Listener
	http      *http.Server
	Name      string
	FailWith  int           // if != 0, respond with this status
	FailBody  string        // optional custom failure body (used when FailWith != 0)
	ResetConn bool          // close connection without a response
	AbortMid  bool          // start SSE then abort
	Stream    bool          // force SSE responses
	Count     atomic.Int64  // requests seen
	LastBody  atomic.Value  // []byte of last request body
	Delay     time.Duration // optional per-request delay
}

// New starts a mock upstream on 127.0.0.1:0. Call Close to stop it.
func New(name string) (*Server, error) {
	return NewAt(name, "127.0.0.1:0")
}

// NewAt starts a mock upstream on the given address (e.g. for a standalone
// harness).
func NewAt(name, addr string) (*Server, error) {
	m := &Server{Name: name}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	m.ln = ln
	mux := http.NewServeMux()
	mux.HandleFunc("/", m.handle)
	m.http = &http.Server{Handler: mux}
	go func() { _ = m.http.Serve(ln) }()
	return m, nil
}

// URL returns the base URL of the mock.
func (m *Server) URL() string { return "http://" + m.ln.Addr().String() }

// Close stops the mock.
func (m *Server) Close() { _ = m.http.Close() }

// SetFail configures failure injection. status 0 = no failure.
func (m *Server) SetFail(status int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.FailWith = status
}

// SetFailBody configures a custom failure response body (with SetFail).
func (m *Server) SetFailBody(body string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.FailBody = body
}

// SetResetConn makes the next requests die at the socket level.
func (m *Server) SetResetConn(on bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ResetConn = on
}

// SetStream forces SSE responses regardless of the request body.
func (m *Server) SetStream(on bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Stream = on
}

// SetAbortMid makes the next request stream a few chunks then abort.
func (m *Server) SetAbortMid(on bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.AbortMid = on
}

// Requests returns the request count.
func (m *Server) Requests() int64 { return m.Count.Load() }

// Body returns the last request body.
func (m *Server) Body() []byte {
	if v := m.LastBody.Load(); v != nil {
		return v.([]byte)
	}
	return nil
}

func (m *Server) handle(w http.ResponseWriter, r *http.Request) {
	m.Count.Add(1)
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	m.LastBody.Store(append([]byte(nil), body...))

	m.mu.Lock()
	fail := m.FailWith
	failBody := m.FailBody
	reset := m.ResetConn
	abort := m.AbortMid
	stream := m.Stream
	delay := m.Delay
	m.mu.Unlock()

	if delay > 0 {
		time.Sleep(delay)
	}
	if reset {
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
			return
		}
	}
	if fail != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(fail)
		if failBody != "" {
			_, _ = w.Write([]byte(failBody))
		} else {
			_, _ = w.Write([]byte(fmt.Sprintf(`{"error":{"message":"mock %s failure %d","type":"mock"}}`, m.Name, fail)))
		}
		return
	}

	streaming := stream
	if !streaming {
		streaming = bytes.Contains(body, []byte(`"stream":true`)) || bytes.Contains(body, []byte(`"stream": true`))
	}
	if !streaming {
		resp := map[string]any{
			"id":      "mock-" + m.Name,
			"object":  "chat.completion",
			"model":   "mock-model",
			"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": "mock response from " + m.Name}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	write := func(ev string) bool {
		if _, err := w.Write([]byte(ev)); err != nil {
			return false
		}
		if flusher != nil {
			flusher.Flush()
		}
		return true
	}
	_ = write("data: " + fmt.Sprintf(`{"id":"mock-%s","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"from-%s"},"finish_reason":null}]}`, m.Name, m.Name) + "\n\n")
	if abort {
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
			return
		}
	}
	_ = write("data: [DONE]\n\n")
}

func contains(b []byte, s string) bool {
	return bytes.Contains(b, []byte(s))
}
