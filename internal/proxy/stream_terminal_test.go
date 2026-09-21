package proxy

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mariobgsp/routre/internal/mock"
)

// captureStderr redirects the two sinks a failed streaming request reports
// through for the duration of fn: reqlog's stderr fallback (os.Stderr, since
// test configs set no request_log) and the standard logger net/http uses for
// recovered handler panics. Both must be inspected together — a recovered
// panic leaves the client response intact, so only the log output reveals it.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	oldStderr := os.Stderr
	oldWriter := log.Writer()
	var logBuf bytes.Buffer
	log.SetOutput(&logBuf)
	os.Stderr = w
	defer func() {
		os.Stderr = oldStderr
		log.SetOutput(oldWriter)
	}()
	fn()
	// Give the handler goroutine a beat to finish its (recovered) panic
	// logging before the pipe closes.
	time.Sleep(150 * time.Millisecond)
	_ = w.Close()
	piped, _ := io.ReadAll(r)
	return string(piped) + logBuf.String()
}

// TestStreamingAllOverloadedSingleWriteNoSleep is the regression test for the
// overloaded-retry double write, updated for the hard failover budget: an
// all-overloaded round no longer sleeps and re-runs. It renders once, as a 503
// with Retry-After: 1, and the upstream sees exactly one request per candidate
// (an immediate identical retry cannot change an overloaded answer).
func TestStreamingAllOverloadedSingleWriteNoSleep(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"model is currently overloaded, try again later"}}`))
	}))
	defer upstream.Close()

	cfg := `{"listen":"127.0.0.1:0",` +
		`"tiers":[{"name":"t","providers":[{"name":"a","kind":"openai","base_url":"` + upstream.URL + `/v1","api_key_env":"TEST_KEY_A","models":["m"]}]}],` +
		`"rtk":{"enabled":false},"cache":{"enabled":true,"max_entries":64,"ttl_seconds":3600}}`
	base, _ := testEnv(t, cfg)

	start := time.Now()
	resp, data := post(t, base, "/v1/chat/completions", chatBody(true, ""))
	elapsed := time.Since(start)
	mu.Lock()
	got := calls
	mu.Unlock()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for an all-overloaded round, got %d: %s", resp.StatusCode, data)
	}
	if got != 1 {
		t.Fatalf("expected exactly one request per candidate (no retry round), upstream saw %d", got)
	}
	if ra := resp.Header.Get("Retry-After"); ra != "1" {
		t.Errorf("Retry-After: want %q, got %q", "1", ra)
	}
	// No sleep, no second round: the whole request must be far under the old
	// 2 x time.Sleep(1s).
	if elapsed > 200*time.Millisecond {
		t.Errorf("all-overloaded round slept: %v (retry rounds must be gone)", elapsed)
	}
}

// TestStreamingUnlistedModelLogsRealStatus covers the other half of the reqlog
// fix: the no-candidate paths (model_not_found / providers_unavailable) render
// a 503 and used to return nil, so `route` logged them as status=200 class="ok"
// and `routre logs -errors` never showed them.
func TestStreamingUnlistedModelLogsRealStatus(t *testing.T) {
	a, _ := mock.New("a")
	defer a.Close()

	// forward_unknown=false + an unlisted model = no eligible candidate.
	base, _ := testEnv(t, cfgFor(t, false, a))

	var resp *http.Response
	var data []byte
	out := captureStderr(t, func() {
		resp, data = post(t, base, "/v1/chat/completions", chatBodyFor("future-x", true))
	})
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for an unlisted model, got %d: %s", resp.StatusCode, data)
	}
	if !strings.Contains(string(data), "model_not_found") {
		t.Fatalf("expected a model_not_found body, got: %s", data)
	}
	if !strings.Contains(out, `"status":503`) || !strings.Contains(out, `"class":"all_failed"`) {
		t.Fatalf("streaming 503 must be logged with its real status/class, got:\n%s", out)
	}
}

// TestStreamingAbortDoesNotPanicHandler locks in the panic that net/http was
// silently recovering: a mid-stream abort used to hand back a typed-nil
// *streamWritten which the reqlog mapper dereferenced. The client response
// looks healthy (partial 200), so only a panic-capturing test detects it.
func TestStreamingAbortDoesNotPanicHandler(t *testing.T) {
	base, _ := streamCacheEnv(t, true) // abortMid

	var resp *http.Response
	var data []byte
	out := captureStderr(t, func() {
		resp, data = post(t, base, "/v1/chat/completions", chatBody(true, ""))
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("mid-stream abort must leave the partial 200 intact, got %d: %s", resp.StatusCode, data)
	}
	if strings.Contains(out, "panic serving") {
		t.Fatalf("mid-stream abort panicked the handler:\n%s", out)
	}
}

// TestWildcardClientErrorStillFailsOver guards the failover contract the fix
// must not break: a deterministic 4xx from a WILDCARD candidate (a model no
// provider lists, forwarded by forward_unknown) is not surfaced — the next
// candidate still gets its turn.
func TestWildcardClientErrorStillFailsOver(t *testing.T) {
	a, _ := mock.New("a")
	defer a.Close()
	a.SetFail(http.StatusBadRequest)
	a.SetFailBody(contextLengthBody)
	b, _ := mock.New("b")
	defer b.Close()

	base, _ := testEnv(t, cfgFor(t, true, a, b))

	resp, data := post(t, base, "/v1/chat/completions", chatBodyFor("future-x", true))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("wildcard 4xx must fail over to b, got %d: %s", resp.StatusCode, data)
	}
	if !strings.Contains(string(data), "from-b") {
		t.Fatalf("expected the stream to come from b, got: %s", data)
	}
	if strings.Contains(string(data), "maximum context length") {
		t.Fatalf("a wildcard 4xx must not be surfaced when another candidate can serve: %s", data)
	}
}
