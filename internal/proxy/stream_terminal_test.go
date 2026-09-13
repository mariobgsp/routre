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

// TestStreamingOverloadedThenClientErrorSingleWrite is the regression test for
// the overloaded-retry double write: when round 1 ends all-overloaded the
// pipeline retries, and if that retry round surfaces a deterministic 4xx
// verbatim, the retry round has ALREADY committed the response. Reassigning
// the retry result and then checking only `len(TryLog) == 0` wrote a second
// status over it — the client got the 400 JSON immediately followed by the
// internal-error JSON, and reqlog recorded a 502 the client never received.
func TestStreamingOverloadedThenClientErrorSingleWrite(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if n <= 2 {
			// Both attempts of round 1: transient capacity, classifies as
			// overloaded so the pipeline takes its retry round.
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"model is currently overloaded, try again later"}}`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(contextLengthBody))
	}))
	defer upstream.Close()

	cfg := `{"listen":"127.0.0.1:0",` +
		`"tiers":[{"name":"t","providers":[{"name":"a","kind":"openai","base_url":"` + upstream.URL + `/v1","api_key_env":"TEST_KEY_A","models":["m"]}]}],` +
		`"rtk":{"enabled":false},"cache":{"enabled":true,"max_entries":64,"ttl_seconds":3600}}`
	base, _ := testEnv(t, cfg)

	resp, data := post(t, base, "/v1/chat/completions", chatBody(true, ""))
	mu.Lock()
	got := calls
	mu.Unlock()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected the retry round's 400 to reach the client, got %d: %s", resp.StatusCode, data)
	}
	if string(data) != contextLengthBody {
		t.Fatalf("the surfaced 4xx must be written exactly once — got a second body appended:\n%s", data)
	}
	if got < 3 {
		t.Fatalf("expected 2 overloaded attempts + 1 retry request, upstream saw %d", got)
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
