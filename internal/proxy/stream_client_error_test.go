package proxy

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/mariobgsp/routre/internal/mock"
)

// contextLengthBody is the shape commandcode returns when a prompt +
// max_tokens overflows the model's context window.
const contextLengthBody = `{"error":{"message":"This model's maximum context length is 1048576 tokens. However, you requested 1052912 tokens (670766 in the messages, 382146 in the completion)","type":"invalid_request_error"}}`

// TestStreamingClientErrorSurfacesUpstreamStatus is the regression test for
// the bug where a streaming request that the listed provider rejects with a
// deterministic 4xx (over-long context, out-of-range max_tokens) came back as
// `503 all_providers_failed` with an EMPTY attempts[] array — hiding the real
// reason and telling the client nothing actionable.
//
// The client must receive the upstream's own status and body.
func TestStreamingClientErrorSurfacesUpstreamStatus(t *testing.T) {
	a, _ := mock.New("a")
	defer a.Close()
	a.SetFail(http.StatusBadRequest)
	a.SetFailBody(contextLengthBody)

	base, _ := testEnv(t, buildConfigWithMocks(t, map[string]*mock.Server{"a": a}))

	resp, data := post(t, base, "/v1/chat/completions", chatBody(true, ""))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected the upstream 400 to reach the client, got %d: %s", resp.StatusCode, data)
	}
	if !strings.Contains(string(data), "maximum context length") {
		t.Fatalf("expected the upstream body verbatim, got: %s", data)
	}
	if strings.Contains(string(data), "all_providers_failed") {
		t.Fatalf("a deterministic client error must not be rendered as all_providers_failed: %s", data)
	}
	if got := resp.Header.Get("Retry-After"); got != "" {
		t.Fatalf("a client error must not advertise Retry-After, got %q", got)
	}
}

// TestStreamingClientErrorSurfaces404 covers the same contract for a model
// the provider does list but rejects as unknown (404) — the class that
// previously produced the same empty-attempts 503.
func TestStreamingClientErrorSurfaces404(t *testing.T) {
	a, _ := mock.New("a")
	defer a.Close()
	a.SetFail(http.StatusNotFound)
	a.SetFailBody(`{"error":{"message":"Model \"m\" is not supported on this endpoint.","type":"invalid_request_error"}}`)

	base, _ := testEnv(t, buildConfigWithMocks(t, map[string]*mock.Server{"a": a}))

	resp, data := post(t, base, "/v1/chat/completions", chatBody(true, ""))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected the upstream 404 to reach the client, got %d: %s", resp.StatusCode, data)
	}
	if !strings.Contains(string(data), "is not supported") {
		t.Fatalf("expected the upstream body verbatim, got: %s", data)
	}
}

// TestStreamingAllFailedCarriesAttempts: when every candidate DID get tried
// and every one failed on a retryable class, the 503 must still carry the
// per-provider attempts[] breakdown — the empty-attempts shape is reserved
// for the internal-error fallback, never for a real outage.
func TestStreamingAllFailedCarriesAttempts(t *testing.T) {
	a, _ := mock.New("a")
	defer a.Close()
	b, _ := mock.New("b")
	defer b.Close()

	base, _ := testEnv(t, buildConfigWithMocks(t, map[string]*mock.Server{"a": a, "b": b}))

	a.SetFail(http.StatusTooManyRequests)
	b.SetFail(http.StatusTooManyRequests)

	resp, data := post(t, base, "/v1/chat/completions", chatBody(true, ""))
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 all_providers_failed, got %d: %s", resp.StatusCode, data)
	}
	var body struct {
		Error struct {
			Type     string `json:"type"`
			Attempts []struct {
				Provider string `json:"provider"`
				Class    string `json:"class"`
			} `json:"attempts"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatalf("all-failed body must be JSON: %v (%s)", err, data)
	}
	if body.Error.Type != "all_providers_failed" {
		t.Fatalf("expected type all_providers_failed, got %q (%s)", body.Error.Type, data)
	}
	if len(body.Error.Attempts) != 2 {
		t.Fatalf("expected one attempt per tried provider (2), got %d: %s", len(body.Error.Attempts), data)
	}
	for _, at := range body.Error.Attempts {
		if at.Provider == "" || at.Class == "" {
			t.Fatalf("attempt entries must name the provider and class, got %+v", at)
		}
	}
}

// TestEmptyAttemptsBodyNamesInternalError: the unreachable "all failed with
// no recorded attempt" state must report itself as a routre bug instead of
// blaming the upstreams with an empty attempts[] array.
func TestEmptyAttemptsBodyNamesInternalError(t *testing.T) {
	raw := emptyAttemptsBody("deepseek/deepseek-v4.1-flash")
	var doc struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
			Model   string `json:"model"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("body must be valid JSON: %v (%s)", err, raw)
	}
	if doc.Error.Type != "internal_error" {
		t.Fatalf("expected type internal_error, got %q", doc.Error.Type)
	}
	if !strings.Contains(doc.Error.Message, "no upstream attempt") {
		t.Fatalf("message must say no attempt was recorded, got %q", doc.Error.Message)
	}
	if doc.Error.Model != "deepseek/deepseek-v4.1-flash" {
		t.Fatalf("expected the model echoed back, got %q", doc.Error.Model)
	}
}

// TestStreamOutcomeEntryMapsRealStatus is the reqlog regression: a streaming
// response that was already written must be logged with the status the client
// actually received (previously every terminal outcome logged 200/ok, so 503s
// were invisible to `routre logs -errors`).
func TestStreamOutcomeEntryMapsRealStatus(t *testing.T) {
	// Success: Stream returns nil.
	if e := streamOutcomeEntry("cli", "m", nil); e.Status != http.StatusOK || e.Class != "ok" || !e.Stream {
		t.Fatalf("nil error must log 200/ok with stream=true, got %d/%s/%v", e.Status, e.Class, e.Stream)
	}
	// Deterministic upstream client error surfaced verbatim.
	e := streamOutcomeEntry("cli", "m", &streamWritten{Status: http.StatusBadRequest, Provider: "commandcode"})
	if e.Status != http.StatusBadRequest || e.Class != "error" || e.Provider != "commandcode" || !e.Stream {
		t.Fatalf("400 must log 400/error with the provider, got %d/%s/%s/%v", e.Status, e.Class, e.Provider, e.Stream)
	}
	// All-failed render.
	e = streamOutcomeEntry("cli", "m", &streamWritten{Status: http.StatusServiceUnavailable})
	if e.Status != http.StatusServiceUnavailable || e.Class != "all_failed" {
		t.Fatalf("503 must log 503/all_failed, got %d/%s", e.Status, e.Class)
	}
	// Internal-error fallback (502, no attempts recorded).
	e = streamOutcomeEntry("cli", "m", &streamWritten{Status: http.StatusBadGateway})
	if e.Status != http.StatusBadGateway || e.Class != "all_failed" {
		t.Fatalf("502 must log 502/all_failed, got %d/%s", e.Status, e.Class)
	}
	// Unknown error (never written): keep the conservative all-failed shape.
	e = streamOutcomeEntry("cli", "m", errors.New("pre-write failure"))
	if e.Status != http.StatusServiceUnavailable || e.Class != "all_failed" {
		t.Fatalf("unknown error must log 503/all_failed, got %d/%s", e.Status, e.Class)
	}
}

// TestStreamOutcomeEntryTypedNilMidStreamAbort is the regression test for the
// panic the handler took on every mid-stream abort: Pipeline.Stream can hand
// back a (*streamWritten)(nil) wrapped in the error interface, and an
// unguarded `sw.Status` dereference inside streamOutcomeEntry crashed the
// request goroutine (recovered by net/http, so only the log line was lost).
// A typed-nil must map to the success shape, never be dereferenced.
func TestStreamOutcomeEntryTypedNilMidStreamAbort(t *testing.T) {
	var typedNil *streamWritten
	if typedNil != nil {
		t.Fatal("precondition: the pointer must be nil")
	}
	// This interface is NOT nil (typed nil inside), which is exactly the
	// shape the abort path produced.
	var err error = typedNil
	if err == nil {
		t.Fatal("precondition: an interface holding a typed-nil pointer is non-nil")
	}
	e := streamOutcomeEntry("cli", "m", err)
	if e.Status != http.StatusOK || e.Class != "ok" || !e.Stream {
		t.Fatalf("typed-nil (mid-stream abort) must log 200/ok, got %d/%s/%v", e.Status, e.Class, e.Stream)
	}
}
