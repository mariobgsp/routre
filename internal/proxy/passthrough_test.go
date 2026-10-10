package proxy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// passthroughConfig builds a tier chain of providers all serving "m", so a
// test can tell WHICH upstream answered from the bytes it returned while
// X-Routre-Model still names the real model.
func passthroughConfig(baseURLs ...string) string {
	var tiers []string
	for i, u := range baseURLs {
		name := string(rune('a' + i))
		tiers = append(tiers, `{"name":"tier-`+name+`","providers":[{"name":"`+name+`","kind":"openai","base_url":"`+u+
			`/v1","api_key_env":"TEST_KEY_`+strings.ToUpper(name)+`","models":["m"]}]}`)
	}
	return `{"listen":"127.0.0.1:0","tiers":[` + strings.Join(tiers, ",") +
		`],"rtk":{"enabled":false},"cache":{"enabled":false}}`
}

// upstream stands in for a provider that answers with a fixed response.
func upstream(t *testing.T, status int, ct, retryAfter, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if retryAfter != "" {
			w.Header().Set("Retry-After", retryAfter)
		}
		if ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

const passthroughBody = `{"error":{"message":"maximum context length is 1048576 tokens","type":"invalid_request_error"}}`

// A deterministic upstream rejection reaches the client byte-for-byte, with
// the real upstream model named. Streaming and buffered agree.
func TestPassthroughDeterministicClientError(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			base, _ := testEnv(t, passthroughConfig(upstream(t, http.StatusBadRequest, "application/json", "", passthroughBody)))
			resp, body := post(t, base, "/v1/chat/completions", chatBody(stream, ""))

			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", resp.StatusCode)
			}
			if string(body) != passthroughBody {
				t.Errorf("body = %q, want the upstream bytes verbatim", body)
			}
			if got := resp.Header.Get("X-Routre-Model"); got != "m" {
				t.Errorf("X-Routre-Model = %q, want m", got)
			}
			if got := resp.Header.Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type = %q, want the upstream value", got)
			}
		})
	}
}

// An empty upstream body is the upstream's answer, not a missing one: routre
// does not invent an envelope to fill the gap.
func TestPassthroughEmptyUpstreamBody(t *testing.T) {
	base, _ := testEnv(t, passthroughConfig(upstream(t, http.StatusBadRequest, "", "", "")))
	resp, body := post(t, base, "/v1/chat/completions", chatBody(false, ""))

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if len(body) != 0 {
		t.Errorf("body = %q, want empty (verbatim upstream)", body)
	}
}

// A rate limit on the first provider is not the client's problem: failover
// runs, and the second provider's deterministic answer is what lands.
func TestPassthroughAfterFailover(t *testing.T) {
	first := upstream(t, http.StatusTooManyRequests, "application/json", "7", `{"error":{"message":"slow down"}}`)
	second := upstream(t, http.StatusServiceUnavailable, "application/json", "", `{"error":{"message":"upstream boom"}}`)

	t.Run("deterministic second answer wins", func(t *testing.T) {
		base, _ := testEnv(t, passthroughConfig(first, upstream(t, http.StatusBadRequest, "application/json", "", passthroughBody)))
		resp, body := post(t, base, "/v1/chat/completions", chatBody(false, ""))
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want the second provider's 400", resp.StatusCode)
		}
		if string(body) != passthroughBody {
			t.Errorf("body = %q, want the second provider's bytes", body)
		}
		if got := resp.Header.Get("X-Routre-Model"); got != "m" {
			t.Errorf("X-Routre-Model = %q, want m", got)
		}
	})

	// Both providers fail retryably: nothing upstream survived to pass
	// through, so routre generates the error.
	t.Run("all retryable failures are routre-generated", func(t *testing.T) {
		base, _ := testEnv(t, passthroughConfig(first, second))
		resp, body := post(t, base, "/v1/chat/completions", chatBody(false, ""))
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503 (routre all-failed)", resp.StatusCode)
		}
		if strings.Contains(string(body), "upstream boom") {
			t.Errorf("body = %q, want a routre envelope, not upstream bytes", body)
		}
		if !strings.Contains(string(body), "all_providers_failed") {
			t.Errorf("body = %q, want all_providers_failed", body)
		}
	})
}

// Upstream Retry-After reaches the client byte-for-byte, in both legal forms,
// alongside a whole-second cooldown.
func TestPassthroughRetryAfter(t *testing.T) {
	t.Run("seconds", func(t *testing.T) {
		base, _ := testEnv(t, passthroughConfig(upstream(t, http.StatusBadRequest, "application/json", "7", passthroughBody)))
		resp, _ := post(t, base, "/v1/chat/completions", chatBody(false, ""))
		if got := resp.Header.Get("Retry-After"); got != "7" {
			t.Errorf("Retry-After = %q, want the upstream %q", got, "7")
		}
		if got := resp.Header.Get("cooldown_seconds"); got != "7" {
			t.Errorf("cooldown_seconds = %q, want 7", got)
		}
	})

	t.Run("http-date", func(t *testing.T) {
		date := time.Now().Truncate(time.Second).Add(31 * time.Second).UTC().Format(http.TimeFormat)
		base, _ := testEnv(t, passthroughConfig(upstream(t, http.StatusBadRequest, "application/json", date, passthroughBody)))
		resp, _ := post(t, base, "/v1/chat/completions", chatBody(false, ""))
		if got := resp.Header.Get("Retry-After"); got != date {
			t.Errorf("Retry-After = %q, want the upstream date %q", got, date)
		}
		secs, err := strconv.Atoi(resp.Header.Get("cooldown_seconds"))
		if err != nil {
			t.Fatalf("cooldown_seconds = %q, want a whole number", resp.Header.Get("cooldown_seconds"))
		}
		if secs < 28 || secs > 31 {
			t.Errorf("cooldown_seconds = %d, want ~30 for an HTTP-date 31s out", secs)
		}
	})
}

// No upstream ever answered: that is the one case routre owes the client a
// body of its own.
func TestPassthroughNoUpstreamResponse(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	base, _ := testEnv(t, passthroughConfig(deadURL))
	resp, body := post(t, base, "/v1/chat/completions", chatBody(false, ""))

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (routre all-failed)", resp.StatusCode)
	}
	if !strings.Contains(string(body), "all_providers_failed") {
		t.Errorf("body = %q, want a routre envelope for a no-response failure", body)
	}
}
