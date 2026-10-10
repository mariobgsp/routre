package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// adaptiveTimeouts is the smallest whole-second adaptive policy: a 1s soft
// slice, one 1s extension, and a 2s header ceiling. Whole seconds because the
// config surface is seconds; the tests below spend a few seconds of wall clock
// to prove the real behaviour.
const adaptiveTimeouts = "\"candidate_seconds\":1,\"extension_seconds\":1,\"header_seconds\":2"

// TestSlowHeadersWithinCeilingSucceeds: a provider that accepts the request and
// answers after its soft slice but inside the ceiling is NOT killed at the
// slice. This is the bug from the request log (358 requests died in the 29-31s
// band while the provider was alive).
func TestSlowHeadersWithinCeilingSucceeds(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(1500 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{\"id\":\"ok\",\"object\":\"chat.completion\",\"choices\":[]}"))
	}))
	defer slow.Close()
	base := gatewayAgainstTimeouts(t, adaptiveTimeouts, "openai", slow.URL)

	start := time.Now()
	resp, data := post(t, base, "/v1/chat/completions", chatBody(false, ""))
	elapsed := time.Since(start)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200 from a slow-but-alive provider, got %d: %s", resp.StatusCode, data)
	}
	if elapsed < 1200*time.Millisecond {
		t.Fatalf("returned after %v; the extension should have waited past the 1s soft slice", elapsed)
	}
}

// TestNoExtensionFailsOverAtSlice is the control: with the extension disabled
// the same 1.5s upstream is abandoned at the 1s slice, which is exactly the
// old (and still available) behaviour.
func TestNoExtensionFailsOverAtSlice(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(1500 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{\"id\":\"ok\",\"object\":\"chat.completion\",\"choices\":[]}"))
	}))
	defer slow.Close()
	base := gatewayAgainstTimeouts(t, "\"candidate_seconds\":1,\"extension_seconds\":0,\"header_seconds\":2", "openai", slow.URL)

	start := time.Now()
	resp, data := post(t, base, "/v1/chat/completions", chatBody(false, ""))
	elapsed := time.Since(start)

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("want 503 when the extension is disabled and no candidate is left, got %d: %s", resp.StatusCode, data)
	}
	if elapsed > 1400*time.Millisecond {
		t.Fatalf("returned after %v; without an extension the 1s slice must bound the wait", elapsed)
	}
}

// TestNoHeadersFailsOverAtCeiling: a socket that accepts the request and never
// answers must still fail over, bounded by the ceiling (1s slice + 1s
// extension = 2s), and reach the healthy provider.
func TestNoHeadersFailsOverAtCeiling(t *testing.T) {
	// Bounded hang: longer than the 2s ceiling, short enough that
	// httptest.Server.Close (which waits for outstanding handlers) returns.
	hung := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	t.Cleanup(hung.Close)
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{\"id\":\"ok\",\"object\":\"chat.completion\",\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1}}"))
	}))
	defer good.Close()
	base := gatewayAgainstTimeouts(t, adaptiveTimeouts, "openai", hung.URL, good.URL)

	start := time.Now()
	resp, data := post(t, base, "/v1/chat/completions", chatBody(false, ""))
	elapsed := time.Since(start)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200 after failing over at the ceiling, got %d: %s", resp.StatusCode, data)
	}
	if got := resp.Header.Get("X-Llrouter-Provider"); got != "p1" {
		t.Fatalf("want provider p1, got %q", got)
	}
	if elapsed < 1500*time.Millisecond || elapsed > 6*time.Second {
		t.Fatalf("failover took %v; want ~2s (1s slice + 1s extension)", elapsed)
	}
}
