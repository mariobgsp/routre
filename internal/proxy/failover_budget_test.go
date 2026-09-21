package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mariobgsp/routre/internal/router"
)

// setBudgets shrinks the failover budgets for a test. Tests in a package run
// sequentially, so the package-level budget vars are safe to swap.
func setBudgets(t *testing.T, candidate, request time.Duration) {
	t.Helper()
	oldC, oldR := candidateFailoverBudget, requestFailoverBudget
	candidateFailoverBudget, requestFailoverBudget = candidate, request
	t.Cleanup(func() { candidateFailoverBudget, requestFailoverBudget = oldC, oldR })
}

// TestFailoverBudgetsAreHardcoded pins the shipped numbers (decision: no
// config keys).
func TestFailoverBudgetsAreHardcoded(t *testing.T) {
	if candidateFailoverBudget != 15*time.Second {
		t.Errorf("candidateFailoverBudget = %v, want 15s", candidateFailoverBudget)
	}
	if requestFailoverBudget != 30*time.Second {
		t.Errorf("requestFailoverBudget = %v, want 30s", requestFailoverBudget)
	}
	if generationBackstop != 5*time.Minute {
		t.Errorf("generationBackstop = %v, want 5m", generationBackstop)
	}
}

// TestRunnerReservesSliceForUntriedCandidates is amendment A3's starvation
// rule: a same-candidate retry may never consume the window an untried healthy
// candidate still needs. Both candidates must be attempted even though the
// first one hangs and asks to retry itself.
func TestRunnerReservesSliceForUntriedCandidates(t *testing.T) {
	setBudgets(t, 40*time.Millisecond, 80*time.Millisecond)
	r := router.New(mkRunnerTiers(), router.DefaultCooldownPolicy())
	runner := newRunner(r, nil)
	cands := r.Candidates("deepseek-v4-flash")
	if len(cands) < 2 {
		t.Fatal("setup: need 2 candidates")
	}
	var mu sync.Mutex
	var tried []string
	got := runner.Run(context.Background(), cands[:2], func(_ context.Context, cand router.Candidate, _ int, budget time.Duration) evalResult {
		mu.Lock()
		tried = append(tried, cand.Provider.Provider.Name)
		mu.Unlock()
		time.Sleep(budget) // a hung provider consumes its whole slice
		return evalResult{Err: errors.New("dial timeout"), Class: router.ErrNetwork, Retryable: true}
	})
	if got.OK {
		t.Fatalf("want not-OK, got %+v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	seen := map[string]bool{}
	for _, n := range tried {
		seen[n] = true
	}
	if len(seen) != 2 {
		t.Fatalf("a candidate was starved by another's retry: tried %v", tried)
	}
}

// TestRunnerBudgetExhaustedSkipsUntriedCandidate proves an overrunning attempt
// yields BudgetExhausted with the right Untried count, which is what the 503
// renderer turns into a failover_budget entry instead of blaming a provider
// that was never asked.
func TestRunnerBudgetExhaustedSkipsUntriedCandidate(t *testing.T) {
	setBudgets(t, 20*time.Millisecond, 40*time.Millisecond)
	r := router.New(mkRunnerTiers(), router.DefaultCooldownPolicy())
	runner := newRunner(r, nil)
	all := r.Candidates("deepseek-v4-flash")
	if len(all) < 3 {
		t.Fatal("setup: need 3 candidates")
	}
	cands := all[:3]
	attempted := 0
	got := runner.Run(context.Background(), cands, func(_ context.Context, _ router.Candidate, _ int, budget time.Duration) evalResult {
		attempted++
		time.Sleep(budget + 30*time.Millisecond) // overrun: the attempt ignores its slice
		return evalResult{Err: errors.New("upstream 503"), Class: router.ErrServer, Retryable: false}
	})
	if !got.BudgetExhausted {
		t.Fatalf("want BudgetExhausted, got %+v", got)
	}
	if got.Untried == 0 {
		t.Fatalf("want Untried > 0, got %+v", got)
	}
	if attempted+got.Untried != len(cands) {
		t.Fatalf("attempted %d + untried %d != %d candidates", attempted, got.Untried, len(cands))
	}
}

// headersThenStallBody commits 200 headers and then never sends a body byte,
// so the gateway's first-byte watchdog is the only thing that can bound it
// (bounded at 2s so httptest.Server.Close can never block).
func headersThenStallBody(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	select {
	case <-r.Context().Done():
	case <-time.After(2 * time.Second):
	}
}

// fastJSON answers immediately with a minimal 200 body.
func fastJSON(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"id":"ok","object":"chat.completion","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
}

// TestNonStreamingBodyStallFailsOverWithinCandidateSlice: headers arrive but
// no first body byte ever does. The candidate's slice must bound that wait —
// not the 5-minute generation backstop — so the request fails over quickly.
// On the pre-fix bound this test would sit on p0 for ~2s (the handler's cap)
// and return p0's result.
func TestNonStreamingBodyStallFailsOverWithinCandidateSlice(t *testing.T) {
	setBudgets(t, 40*time.Millisecond, 200*time.Millisecond)
	stalled := httptest.NewServer(http.HandlerFunc(headersThenStallBody))
	defer stalled.Close()
	good := httptest.NewServer(http.HandlerFunc(fastJSON))
	defer good.Close()
	base := gatewayAgainst(t, "openai", stalled.URL, good.URL)

	start := time.Now()
	resp, data := post(t, base, "/v1/chat/completions", chatBody(false, ""))
	elapsed := time.Since(start)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200 from the healthy provider after a body stall, got %d: %s", resp.StatusCode, data)
	}
	if got := resp.Header.Get("X-Llrouter-Provider"); got != "p1" {
		t.Fatalf("want failover to p1, got provider %q", got)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("body stall took %v; the candidate slice (40ms), not the 5m backstop, must bound the first body byte", elapsed)
	}
}

// TestFirstByteBudgetIsSpentOncePerCandidate: the header wait and the first
// body byte draw on the SAME candidate slice. With a 300ms slice and a ~290ms
// header wait, the body stall must be abandoned at ~300ms — not at the ~590ms
// a second full slice would allow. The slice is deliberately long relative to
// the assertion margin so a loaded CI runner cannot flake it.
func TestFirstByteBudgetIsSpentOncePerCandidate(t *testing.T) {
	setBudgets(t, 300*time.Millisecond, 900*time.Millisecond)
	slowHeaders := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(290 * time.Millisecond) // spends most of the candidate's slice
		headersThenStallBody(w, r)
	}))
	defer slowHeaders.Close()
	good := httptest.NewServer(http.HandlerFunc(fastJSON))
	defer good.Close()
	base := gatewayAgainst(t, "openai", slowHeaders.URL, good.URL)

	start := time.Now()
	resp, data := post(t, base, "/v1/chat/completions", chatBody(false, ""))
	elapsed := time.Since(start)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200 from p1, got %d: %s", resp.StatusCode, data)
	}
	if elapsed > 420*time.Millisecond {
		t.Fatalf("candidate spent %v; the header wait and the first byte must fit ONE 300ms slice (~590ms means the slice was spent twice)", elapsed)
	}
}

func gatewayAgainst(t *testing.T, kind string, urls ...string) string {
	t.Helper()
	keys := []string{"TEST_KEY_A", "TEST_KEY_B", "TEST_KEY_C"}
	providers := make([]string, 0, len(urls))
	for i, u := range urls {
		t.Setenv(keys[i], "test-key")
		providers = append(providers, fmt.Sprintf(
			`{"name":"p%d","kind":"%s","base_url":"%s/v1","api_key_env":"%s","models":["m"]}`, i, kind, u, keys[i]))
	}
	cfg := `{"listen":"127.0.0.1:0","tiers":[{"name":"t","providers":[` + strings.Join(providers, ",") +
		`]}],"rtk":{"enabled":false},"cache":{"enabled":false}}`
	base, _ := testEnv(t, cfg)
	return base
}

// hangHeaders is a handler that never responds: it holds until the gateway
// gives up (or a hard 2s cap, so httptest.Server.Close can never block).
func hangHeaders(_ http.ResponseWriter, r *http.Request) {
	select {
	case <-r.Context().Done():
	case <-time.After(2 * time.Second):
	}
}

// TestFailoverBudgetBoundHungProvider: two providers that accept the
// connection and then send nothing. The header watchdog must fail over at the
// candidate budget, so the whole request is bounded and each provider is
// asked exactly once.
func TestFailoverBudgetBoundHungProvider(t *testing.T) {
	setBudgets(t, 20*time.Millisecond, 40*time.Millisecond)
	a := httptest.NewServer(http.HandlerFunc(hangHeaders))
	defer a.Close()
	b := httptest.NewServer(http.HandlerFunc(hangHeaders))
	defer b.Close()
	base := gatewayAgainst(t, "openai", a.URL, b.URL)

	start := time.Now()
	resp, data := post(t, base, "/v1/chat/completions", chatBody(false, ""))
	elapsed := time.Since(start)

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("want 503 from two hung providers, got %d: %s", resp.StatusCode, data)
	}
	if elapsed > 3*requestFailoverBudget {
		t.Fatalf("time to failure %v exceeded 3x the request budget %v", elapsed, requestFailoverBudget)
	}
}

// TestStreamFailoverBudgetBoundHungProvider: same over SSE. The first-byte
// watchdog must bound the stream too.
func TestStreamFailoverBudgetBoundHungProvider(t *testing.T) {
	setBudgets(t, 20*time.Millisecond, 40*time.Millisecond)
	a := httptest.NewServer(http.HandlerFunc(hangHeaders))
	defer a.Close()
	b := httptest.NewServer(http.HandlerFunc(hangHeaders))
	defer b.Close()
	base := gatewayAgainst(t, "openai", a.URL, b.URL)

	start := time.Now()
	resp, data := post(t, base, "/v1/chat/completions", chatBody(true, ""))
	elapsed := time.Since(start)

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("want 503 from two hung streams, got %d: %s", resp.StatusCode, data)
	}
	if elapsed > 3*requestFailoverBudget {
		t.Fatalf("stream time to failure %v exceeded 3x the request budget %v", elapsed, requestFailoverBudget)
	}
}

// TestNonStreamingFirstByteTimeoutFailsOver: a provider that never sends
// headers fails over to a healthy one instead of hanging the client.
func TestNonStreamingFirstByteTimeoutFailsOver(t *testing.T) {
	setBudgets(t, 20*time.Millisecond, 60*time.Millisecond)
	hung := httptest.NewServer(http.HandlerFunc(hangHeaders))
	defer hung.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok","object":"chat.completion","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer good.Close()
	base := gatewayAgainst(t, "openai", hung.URL, good.URL)

	resp, data := post(t, base, "/v1/chat/completions", chatBody(false, ""))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200 after failing over a hung provider, got %d: %s", resp.StatusCode, data)
	}
	if got := resp.Header.Get("X-Llrouter-Provider"); got != "p1" {
		t.Fatalf("want provider p1, got %q", got)
	}
}

// TestNonStreamingLongGenerationSurvives: the candidate budget bounds the
// header wait and the FIRST body byte, not the whole generation. Once a first
// byte has arrived, the rest of the body may take as long as the generation
// backstop allows.
func TestNonStreamingLongGenerationSurvives(t *testing.T) {
	setBudgets(t, 20*time.Millisecond, 40*time.Millisecond)
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// The first body byte (plus flush) is what disarms the first-byte
		// watchdog; the remaining generation may then exceed the budget.
		_, _ = w.Write([]byte("{"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(80 * time.Millisecond) // well past the 20ms candidate budget
		_, _ = w.Write([]byte(`"id":"slow","object":"chat.completion","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer slow.Close()
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer other.Close()
	base := gatewayAgainst(t, "openai", slow.URL, other.URL)

	resp, data := post(t, base, "/v1/chat/completions", chatBody(false, ""))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a long generation must survive the candidate budget, got %d: %s", resp.StatusCode, data)
	}
	if got := resp.Header.Get("X-Llrouter-Provider"); got != "p0" {
		t.Fatalf("want the slow provider p0 to serve, got %q", got)
	}
}

// TestNoOverloadSleepOnAllOverloaded: an all-overloaded round renders once,
// immediately, with Retry-After: 1 — no sleep, no second candidate round.
func TestNoOverloadSleepOnAllOverloaded(t *testing.T) {
	setBudgets(t, 50*time.Millisecond, 200*time.Millisecond)
	var mu sync.Mutex
	counts := map[string]int{}
	overloaded := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			mu.Lock()
			counts[name]++
			mu.Unlock()
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"model is currently overloaded, try again later"}}`))
		}
	}
	a := httptest.NewServer(overloaded("a"))
	defer a.Close()
	b := httptest.NewServer(overloaded("b"))
	defer b.Close()
	base := gatewayAgainst(t, "openai", a.URL, b.URL)

	start := time.Now()
	resp, data := post(t, base, "/v1/chat/completions", chatBody(false, ""))
	elapsed := time.Since(start)

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d: %s", resp.StatusCode, data)
	}
	if ra := resp.Header.Get("Retry-After"); ra != "1" {
		t.Errorf("Retry-After: want %q, got %q", "1", ra)
	}
	mu.Lock()
	defer mu.Unlock()
	if counts["a"] != 1 || counts["b"] != 1 {
		t.Fatalf("want one request per candidate (no retry round), got a=%d b=%d", counts["a"], counts["b"])
	}
	if elapsed > 200*time.Millisecond {
		t.Errorf("all-overloaded round slept: %v", elapsed)
	}
}

// TestFailoverBudgetOutcomeOnOverrun: when an attempt overruns its slice the
// next candidate is never tried, and the 503 must say so with a
// failover_budget entry rather than blaming a provider.
func TestFailoverBudgetOutcomeOnOverrun(t *testing.T) {
	setBudgets(t, 20*time.Millisecond, 40*time.Millisecond)
	// Commits a 503 status immediately, then stalls the body past the request
	// budget. The attempt fails after the deadline, so the second provider is
	// never tried.
	slowFail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(150 * time.Millisecond)
		_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
	}))
	defer slowFail.Close()
	var mu sync.Mutex
	bCalls := 0
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		bCalls++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer second.Close()
	base := gatewayAgainst(t, "openai", slowFail.URL, second.URL)

	resp, data := post(t, base, "/v1/chat/completions", chatBody(false, ""))
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d: %s", resp.StatusCode, data)
	}
	if !strings.Contains(string(data), "failover_budget") {
		t.Fatalf("expected a failover_budget attempts entry, got: %s", data)
	}
	mu.Lock()
	defer mu.Unlock()
	if bCalls != 0 {
		t.Fatalf("the second provider was tried after the budget was exhausted (calls=%d)", bCalls)
	}
}

// TestFirstByteWatchdogNoCloseAfterFirstByte is the race fix: when the reader
// wins, the watchdog must never close the body, even after the timeout would
// have fired.
func TestFirstByteWatchdogNoCloseAfterFirstByte(t *testing.T) {
	rc := &countingCloser{ReadCloser: io.NopCloser(strings.NewReader("hello"))}
	fb := watchFirstByte(rc, 5*time.Millisecond)
	defer fb.timer.Stop()

	buf := make([]byte, 5)
	if n, err := fb.Read(buf); n != 5 || err != nil {
		t.Fatalf("first Read: n=%d err=%v, want 5/nil", n, err)
	}
	// Let the (stopped) timer's deadline pass: it must not close the body.
	time.Sleep(25 * time.Millisecond)
	if c := rc.CloseCount(); c != 0 {
		t.Fatalf("watchdog closed a live body after a byte arrived (closed=%d)", c)
	}
	if fb.timedOut.Load() {
		t.Fatal("timedOut set even though a byte arrived first")
	}
}

// TestFirstByteWatchdogTimeoutWins: when the timeout wins it closes the body
// once and a later byte is not surfaced as a successful start.
func TestFirstByteWatchdogTimeoutWins(t *testing.T) {
	rc := &countingCloser{ReadCloser: io.NopCloser(strings.NewReader("later"))}
	fb := watchFirstByte(rc, 5*time.Millisecond)
	defer fb.timer.Stop()

	time.Sleep(25 * time.Millisecond)
	if !fb.timedOut.Load() {
		t.Fatal("timedOut not set after the watchdog fired")
	}
	if c := rc.CloseCount(); c != 1 {
		t.Fatalf("watchdog closed %d times, want 1", c)
	}
	n, err := fb.Read(make([]byte, 8))
	if n != 0 || !errors.Is(err, errFirstByteTimeout) {
		t.Fatalf("byte surfaced after the timeout won: n=%d err=%v", n, err)
	}
}
