package proxy

import (
	"sync"
	"testing"

	"github.com/mariobgsp/routre/internal/mock"
	"github.com/mariobgsp/routre/internal/tokenize"
)

// TestNoExactWholeBodyCountOnRequestPath is the behavioural half of the
// counting guard: it drives a real 1 MiB request through the pipeline and
// fails if any exact BPE count above the cap runs. The small-body request
// proves the observer is actually wired (a vacuous pass is worse than no
// guard). Exact counting of an oversized body is what put ~800 ms of
// gateway-added latency on every tool-heavy request.
func TestNoExactWholeBodyCountOnRequestPath(t *testing.T) {
	up, err := mock.New("a")
	if err != nil {
		t.Fatalf("mock upstream: %v", err)
	}
	defer up.Close()
	base, _ := testEnv(t, buildMockConfig(t, "openai", map[string]*mock.Server{"a": up}))

	var mu sync.Mutex
	calls, maxLen := 0, 0
	tokenize.SetCountObserver(func(n int) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if n > maxLen {
			maxLen = n
		}
	})
	defer tokenize.SetCountObserver(nil)

	// Below the cap the path must still count (proves the guard is live).
	small := fixtureToolHeavyBody(t, 8<<10, nextFixtureNonce())
	if resp, _ := post(t, base, "/v1/chat/completions", small); resp.StatusCode != 200 {
		t.Fatalf("small body: status %d", resp.StatusCode)
	}
	mu.Lock()
	smallCalls, smallMax := calls, maxLen
	mu.Unlock()
	if smallCalls == 0 {
		t.Fatal("guard is vacuous: no exact count ran on an 8 KiB body")
	}
	if smallMax > tokenize.ExactLimit {
		t.Fatalf("small body counted %d bytes, cap %d", smallMax, tokenize.ExactLimit)
	}

	large := fixtureToolHeavyBody(t, 1<<20, nextFixtureNonce())
	if resp, _ := post(t, base, "/v1/chat/completions", large); resp.StatusCode != 200 {
		t.Fatalf("large body: status %d", resp.StatusCode)
	}
	mu.Lock()
	defer mu.Unlock()
	if maxLen > tokenize.ExactLimit {
		t.Fatalf("an exact whole-body count of %d bytes ran on the request path (cap %d)",
			maxLen, tokenize.ExactLimit)
	}
}
