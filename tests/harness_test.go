// Package tests holds routre's end-to-end suite.
//
// Unlike the unit tests under internal/, these tests drive the real
// `routre` binary as a child process: they write a config file, boot
// `routre serve`, and talk to it over HTTP exactly like a coding agent
// would. That is the only layer that proves the shipped artifact works —
// flag parsing, config loading, listen address, the HTTP surface, and
// process shutdown are all real here, not simulated.
//
// What does NOT belong here: pure-function checks (BPE token counts, RTK
// compression ratio, keystore crypto, dialect JSON translation). Those
// have no e2e equivalent — an e2e test of a token count is slower and
// less precise — so they stay colocated in internal/ as unit tests.
//
// Usage:
//
//	go test ./tests -v          # e2e only
//	go test ./...               # everything
package tests

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// binOnce builds the routre binary exactly once for the whole package.
var (
	binOnce sync.Once
	binPath string
	binErr  error
)

// routreBinary builds ./ into a temp dir and returns its path.
func routreBinary(t testing.TB) string {
	t.Helper()
	binOnce.Do(func() {
		dir, err := os.MkdirTemp("", "routre-e2e-bin-")
		if err != nil {
			binErr = err
			return
		}
		out := filepath.Join(dir, "routre")
		cmd := exec.Command("go", "build", "-o", out, ".")
		cmd.Dir = repoRoot()
		if b, err := cmd.CombinedOutput(); err != nil {
			binErr = fmt.Errorf("go build: %v\n%s", err, b)
			return
		}
		binPath = out
	})
	if binErr != nil {
		t.Fatalf("build routre: %v", binErr)
	}
	return binPath
}

// repoRoot returns the module root (parent of the tests/ dir).
func repoRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		return ".."
	}
	return filepath.Dir(wd)
}

// Gateway is a booted `routre serve` child process plus the mock upstreams
// it talks to.
type Gateway struct {
	Base  string           // e.g. http://127.0.0.1:39411
	Mocks map[string]*Mock // by provider name
	Data  string           // isolated ROUTRE_CLI_DATA_DIR
	env   []string         // full child env, reused by Run

	cmd  *exec.Cmd
	log  *lineLog
	data string
	cfg  string
}

// Mock is one upstream provider stub. It is an HTTP server on 127.0.0.1
// that records what it received and can be told to fail.
type Mock struct {
	Name string
	srv  *http.Server
	ln   net.Listener

	mu       sync.Mutex
	fail     int
	failBody string
	failOnce bool
	stream   bool
	anthro   bool
	gemini   bool
	delay    time.Duration
	models   []string

	requests int64
	lastBody []byte
	lastHdr  http.Header
}

// GWOpts configures a Gateway under construction.
type GWOpts struct {
	// Providers, in failover order. Names must be a/b/c to match the
	// TEST_KEY_A/B/C env vars the config references.
	Providers []string
	// Kind is the upstream dialect: openai, anthropic, gemini.
	Kind string
	// Models is the model list each provider advertises.
	Models []string
	// AuthSecretEnv, when set, enables gateway auth using that env var.
	AuthSecretEnv string
	// Extra is merged into the config root (e.g. rtk/cache overrides).
	Extra map[string]any
}

// StartGateway builds the config, boots the binary, and waits for /healthz.
func StartGateway(t testing.TB, o GWOpts) *Gateway {
	t.Helper()
	if o.Kind == "" {
		o.Kind = "openai"
	}
	if o.Models == nil {
		o.Models = []string{"m"}
	}

	dir := t.TempDir()
	g := &Gateway{Mocks: map[string]*Mock{}, data: dir}

	// Mock upstreams first: their URLs go into the config.
	var provs []string
	for _, name := range o.Providers {
		m := newMock(t, name)
		// The mock must speak the same dialect the config claims, or the
		// gateway is translating a shape the provider never sent.
		switch o.Kind {
		case "anthropic":
			m.SetAnthropic(true)
		case "gemini":
			m.SetGemini(true)
		}
		g.Mocks[name] = m
		provs = append(provs, fmt.Sprintf(
			`{"name":%q,"kind":%q,"base_url":%q,"api_key_env":"TEST_KEY_%s","models":%s}`,
			name, o.Kind, m.URL()+"/v1", strings.ToUpper(name), mustJSON(o.Models)))
	}
	if len(provs) == 0 {
		t.Fatal("StartGateway: need at least one provider")
	}

	root := map[string]any{
		"listen":          "127.0.0.1:0",
		"forward_unknown": true,
		"tiers":           []any{map[string]any{"name": "e2e", "providers": provsRaw(provs)}},
		"rtk":             map[string]any{"enabled": true, "min_bytes": 0, "max_bytes": 10485760},
		"cache":           map[string]any{"enabled": true, "max_entries": 64, "ttl_seconds": 3600},
	}
	if o.AuthSecretEnv != "" {
		root["auth"] = map[string]any{"secret_env": o.AuthSecretEnv}
	}
	for k, v := range o.Extra {
		root[k] = v
	}
	g.cfg = filepath.Join(dir, "config.json")
	if err := os.WriteFile(g.cfg, []byte(mustJSON(root)), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Provider keys must be in the child's env.
	env := append(os.Environ(), "ROUTRE_CLI_DATA_DIR="+g.data)
	for _, name := range o.Providers {
		env = append(env, "TEST_KEY_"+strings.ToUpper(name)+"=test-key-"+name)
	}
	if o.AuthSecretEnv != "" {
		env = append(env, o.AuthSecretEnv+"=e2e-secret")
	}
	g.env = env

	g.log = newLineLog()
	cmd := exec.Command(routreBinary(t), "serve", "-config", g.cfg)
	cmd.Env = env
	cmd.Stdout = g.log
	cmd.Stderr = g.log
	if err := cmd.Start(); err != nil {
		t.Fatalf("start serve: %v", err)
	}
	g.cmd = cmd
	t.Cleanup(func() { g.Stop() })

	addr := g.waitListen(t)
	g.Base = "http://" + addr
	g.waitHealthy(t)
	return g
}

// provsRaw is a no-op marker: provs are already JSON strings and get
// embedded verbatim, so the caller wraps them in a raw array.
func provsRaw(provs []string) []any {
	out := make([]any, 0, len(provs))
	for _, p := range provs {
		out = append(out, json.RawMessage(p))
	}
	return out
}

// Stop terminates the child process and all mock upstreams.
func (g *Gateway) Stop() {
	if g == nil {
		return
	}
	if g.cmd != nil && g.cmd.Process != nil {
		_ = g.cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _, _ = g.cmd.Process.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = g.cmd.Process.Kill()
		}
		g.cmd = nil
	}
	for _, m := range g.Mocks {
		m.Close()
	}
}

// waitListen blocks until serve logs its bound address.
var listenRe = regexp.MustCompile(`listening on ([0-9.]+:[0-9]+)`)

func (g *Gateway) waitListen(t testing.TB) string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		for _, line := range g.log.Lines() {
			if m := listenRe.FindStringSubmatch(line); m != nil {
				return m[1]
			}
		}
		if g.cmd.ProcessState != nil {
			t.Fatalf("serve exited early:\n%s", g.log.Text())
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("serve never reported a listen address:\n%s", g.log.Text())
	return ""
}

// waitHealthy polls /healthz until the gateway answers.
func (g *Gateway) waitHealthy(t testing.TB) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(g.Base + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("gateway never became healthy:\n%s", g.log.Text())
}

// Run executes a routre subcommand against this gateway's config and
// returns combined output. Used for the CLI-level checks.
func (g *Gateway) Run(t testing.TB, args ...string) (string, error) {
	t.Helper()
	full := append([]string{args[0], "-config", g.cfg}, args[1:]...)
	cmd := exec.Command(routreBinary(t), full...)
	cmd.Env = g.env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// ---------------------------------------------------------------------------
// HTTP helpers
// ---------------------------------------------------------------------------

// Post sends a JSON POST and returns status + body.
func (g *Gateway) Post(t testing.TB, path string, body any, hdrs map[string]string) (int, []byte) {
	t.Helper()
	raw, ok := body.([]byte)
	if !ok {
		raw = []byte(mustJSON(body))
	}
	req, err := http.NewRequest(http.MethodPost, g.Base+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdrs {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post %s: %v", path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

// PostH is Post plus the response header, for tests that assert on
// gateway-emitted headers (X-Llrouter-Cache, X-Llrouter-Provider,
// Retry-After).
func (g *Gateway) PostH(t testing.TB, path string, body any, hdrs map[string]string) (int, []byte, http.Header) {
	t.Helper()
	raw, ok := body.([]byte)
	if !ok {
		raw = []byte(mustJSON(body))
	}
	req, err := http.NewRequest(http.MethodPost, g.Base+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdrs {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post %s: %v", path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data, resp.Header
}

// Get issues a GET and returns status + body.
func (g *Gateway) Get(t testing.TB, path string) (int, []byte) {
	t.Helper()
	resp, err := http.Get(g.Base + path)
	if err != nil {
		t.Fatalf("get %s: %v", path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

// PostStream sends a streaming request and returns the raw SSE text.
func (g *Gateway) PostStream(t testing.TB, path string, body any) (int, string) {
	t.Helper()
	raw := []byte(mustJSON(body))
	req, err := http.NewRequest(http.MethodPost, g.Base+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post %s: %v", path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	return resp.StatusCode, string(data)
}

// ChatBody builds an OpenAI chat request for model "m".
func ChatBody(stream bool) map[string]any {
	b := map[string]any{
		"model":    "m",
		"messages": []any{map[string]any{"role": "user", "content": "hello"}},
	}
	if stream {
		b["stream"] = true
	}
	return b
}

// ---------------------------------------------------------------------------
// Mock upstream
// ---------------------------------------------------------------------------

func newMock(t testing.TB, name string) *Mock {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("mock listen: %v", err)
	}
	m := &Mock{Name: name, ln: ln, models: []string{"m"}}
	mux := http.NewServeMux()
	mux.HandleFunc("/", m.handle)
	m.srv = &http.Server{Handler: mux}
	go func() { _ = m.srv.Serve(ln) }()
	return m
}

// URL is the mock's base address.
func (m *Mock) URL() string { return "http://" + m.ln.Addr().String() }

// Close stops the mock.
func (m *Mock) Close() { _ = m.srv.Close() }

// SetFail makes the mock answer with the given status (0 = healthy).
func (m *Mock) SetFail(status int) {
	m.mu.Lock()
	m.fail = status
	m.mu.Unlock()
}

// SetFailBody sets a custom failure body.
func (m *Mock) SetFailBody(b string) {
	m.mu.Lock()
	m.failBody = b
	m.mu.Unlock()
}

// SetFailOnce fails the next request only, then recovers. Deterministic —
// the failure clears itself when it is served, so no sleep races the
// gateway's own retry timing.
func (m *Mock) SetFailOnce(status int) {
	m.mu.Lock()
	m.fail, m.failBody, m.failOnce = status, "", true
	m.mu.Unlock()
}

// SetStream forces SSE responses.
func (m *Mock) SetStream(on bool) {
	m.mu.Lock()
	m.stream = on
	m.mu.Unlock()
}

// SetAnthropic emits Anthropic-style SSE frames.
func (m *Mock) SetAnthropic(on bool) {
	m.mu.Lock()
	m.anthro = on
	m.mu.Unlock()
}

// SetGemini emits Gemini generateContent responses.
func (m *Mock) SetGemini(on bool) {
	m.mu.Lock()
	m.gemini = on
	m.mu.Unlock()
}

// SetDelay adds a per-request delay.
func (m *Mock) SetDelay(d time.Duration) {
	m.mu.Lock()
	m.delay = d
	m.mu.Unlock()
}

// SetModels sets the /v1/models list.
func (m *Mock) SetModels(ids []string) {
	m.mu.Lock()
	m.models = append([]string(nil), ids...)
	m.mu.Unlock()
}

// Requests is how many requests the mock has served.
func (m *Mock) Requests() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.requests
}

// LastBody is the most recent request body.
func (m *Mock) LastBody() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]byte(nil), m.lastBody...)
}

// LastHeader is the most recent request header.
func (m *Mock) LastHeader() http.Header {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastHdr.Clone()
}

func (m *Mock) handle(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	m.requests++
	m.lastBody, _ = io.ReadAll(io.LimitReader(r.Body, 1<<20))
	m.lastHdr = r.Header.Clone()
	fail, failBody, failOnce := m.fail, m.failBody, m.failOnce
	stream, anthro, gemini, delay := m.stream, m.anthro, m.gemini, m.delay
	models := append([]string(nil), m.models...)
	if failOnce {
		// Self-clearing: the single failure is consumed as it is served.
		m.fail, m.failOnce = 0, false
	}
	m.mu.Unlock()

	if r.URL.Path == "/v1/models" {
		w.Header().Set("Content-Type", "application/json")
		data := make([]map[string]any, 0, len(models))
		for _, id := range models {
			data = append(data, map[string]any{"id": id, "object": "model"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
		return
	}
	if delay > 0 {
		time.Sleep(delay)
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

	streaming := stream || bytes.Contains(m.lastBody, []byte(`"stream":true`)) || bytes.Contains(m.lastBody, []byte(`"stream": true`))
	if gemini {
		m.writeGemini(w, streaming)
		return
	}
	if !streaming {
		w.Header().Set("Content-Type", "application/json")
		if anthro {
			// Non-streaming Anthropic /v1/messages shape, for a same-kind
			// anthropic client -> anthropic provider path.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "msg_" + m.Name, "type": "message", "role": "assistant", "model": "mock-model",
				"content":     []any{map[string]any{"type": "text", "text": "mock response from " + m.Name}},
				"stop_reason": "end_turn",
				"usage":       map[string]any{"input_tokens": 10, "output_tokens": 5},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "mock-" + m.Name, "object": "chat.completion", "model": "mock-model",
			"choices": []any{map[string]any{"index": 0,
				"message":       map[string]any{"role": "assistant", "content": "mock response from " + m.Name},
				"finish_reason": "stop"}},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
		})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	evs := []string{
		fmt.Sprintf(`data: {"id":"mock-%s","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"from-%s"},"finish_reason":null}]}`, m.Name, m.Name),
		`data: {"id":"mock-` + m.Name + `","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}]}`,
		fmt.Sprintf(`data: {"id":"mock-%s","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, m.Name),
		"data: [DONE]",
	}
	if anthro {
		evs = []string{
			`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"msg_` + m.Name + `","type":"message","role":"assistant","model":"mock-model","content":[],"usage":{"input_tokens":10,"output_tokens":0}}}`,
			`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"from-` + m.Name + `"}}`,
			`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}`,
			`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}`,
			`event: message_stop` + "\n" + `data: {"type":"message_stop"}`,
		}
	}
	for _, ev := range evs {
		if _, err := io.WriteString(w, ev+"\n\n"); err != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
}

func (m *Mock) writeGemini(w http.ResponseWriter, streaming bool) {
	if !streaming {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"candidates": []any{map[string]any{"content": map[string]any{
				"role": "model", "parts": []any{map[string]any{"text": "mock response from " + m.Name}}},
				"finishReason": "STOP"}},
			"usageMetadata": map[string]any{"promptTokenCount": 10, "candidatesTokenCount": 5, "totalTokenCount": 15},
		})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	for _, ev := range []string{
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"from-` + m.Name + `"}]},"index":0}]}`,
		`data: {"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP","index":0}],"usageMetadata":{"candidatesTokenCount":5}}`,
	} {
		if _, err := io.WriteString(w, ev+"\n\n"); err != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
}

// ---------------------------------------------------------------------------
// Small helpers
// ---------------------------------------------------------------------------

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// Decode unmarshals a response body, failing the test on bad JSON.
func Decode(t testing.TB, data []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("decode: %v\nbody: %s", err, data)
	}
	return m
}

// lineLog collects a child process's output line by line.
type lineLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func newLineLog() *lineLog { return &lineLog{} }

func (l *lineLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *lineLog) Lines() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Split(strings.TrimRight(l.buf.String(), "\n"), "\n")
}

func (l *lineLog) Text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}
