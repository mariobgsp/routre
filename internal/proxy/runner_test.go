package proxy

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mariobgsp/routre/internal/router"
)

// mkRunnerTiers mirrors the production router test fixture enough to
// exercise the runner: 3 candidates for "deepseek-v4-flash", 2 for
// "hy3". Kept local because the router package's mkModelTiers lives
// in a _test.go file and isn't importable.
func mkRunnerTiers() []router.TierInput {
	return []router.TierInput{
		{Name: "subscription", Providers: []router.ProviderInput{
			{Name: "opencode-go", Kind: "openai", BaseURL: "https://go", APIKeyEnv: "GO", Models: []string{"deepseek-v4-flash", "hy3"}},
			{Name: "opencode-zen", Kind: "openai", BaseURL: "https://zen", APIKeyEnv: "ZEN", Models: []string{"deepseek-v4-flash", "hy3"}},
		}},
		{Name: "openrouter", Providers: []router.ProviderInput{
			{Name: "openrouter", Kind: "openai", BaseURL: "https://or", APIKeyEnv: "OR", Models: []string{"deepseek/deepseek-v4-flash:free"}},
		}},
	}
}

// TestRunnerSuccessFirstCand: the eval succeeds on the first attempt of
// the first candidate. Runner returns OK=true, no TryLog.
func TestRunnerSuccessFirstCand(t *testing.T) {
	r := router.New(mkRunnerTiers(), router.DefaultCooldownPolicy())
	runner := newRunner(r, nil)

	want := &Response{StatusCode: 200, Body: []byte(`{"ok":true}`)}
	cands := r.Candidates("deepseek-v4-flash")
	if len(cands) == 0 {
		t.Fatal("setup: expected candidates")
	}
	got := runner.Run(context.Background(), cands, func(ctx context.Context, cand router.Candidate, attempt int) evalResult {
		return evalResult{OK: true, Response: want}
	})
	if !got.OK {
		t.Fatalf("want OK, got %+v", got)
	}
	if got.Response != want {
		t.Errorf("response: want %p, got %p", want, got.Response)
	}
	if len(got.TryLog) != 0 {
		t.Errorf("tryLog: want empty, got %+v", got.TryLog)
	}
}

// TestRunnerFailoverOnRetryable: cand 0 fails (non-retryable so the
// inner loop exits immediately); cand 1 succeeds. Runner produces a
// one-entry TryLog (the failed cand) and returns OK.
func TestRunnerFailoverOnRetryable(t *testing.T) {
	r := router.New(mkRunnerTiers(), router.DefaultCooldownPolicy())
	runner := newRunner(r, nil)
	cands := r.Candidates("deepseek-v4-flash")
	if len(cands) < 2 {
		t.Fatal("setup: need at least 2 candidates")
	}
	calls := 0
	got := runner.Run(context.Background(), cands, func(ctx context.Context, cand router.Candidate, attempt int) evalResult {
		calls++
		if calls == 1 {
			// Non-retryable so the runner records the cand in
			// tryLog and moves on. (Retryable would loop the
			// inner retry budget on cand 0; see TestRunnerRetryThenFail.)
			return evalResult{Err: errors.New("upstream 503"), Class: router.ErrServer, Retryable: false}
		}
		return evalResult{OK: true, Response: &Response{StatusCode: 200}}
	})
	if !got.OK {
		t.Fatalf("want OK, got %+v", got)
	}
	if len(got.TryLog) != 1 {
		t.Errorf("tryLog: want 1 entry, got %d", len(got.TryLog))
	}
	if got.TryLog[0].Err != "upstream 503" {
		t.Errorf("tryLog[0].Err: want %q, got %q", "upstream 503", got.TryLog[0].Err)
	}
}

// TestRunnerRetryThenFail: every attempt is retryable, so the inner
// loop exhausts the retry budget on each cand before moving to the
// next. Total eval calls = len(cands) * (1 + retryTransientAttempts).
func TestRunnerRetryThenFail(t *testing.T) {
	r := router.New(mkRunnerTiers(), router.DefaultCooldownPolicy())
	runner := newRunner(r, nil)
	cands := r.Candidates("deepseek-v4-flash")
	if len(cands) == 0 {
		t.Fatal("setup: expected candidates")
	}
	calls := 0
	got := runner.Run(context.Background(), cands, func(ctx context.Context, cand router.Candidate, attempt int) evalResult {
		calls++
		return evalResult{Err: errors.New("transient"), Class: router.ErrServer, Retryable: true}
	})
	if got.OK {
		t.Fatalf("want not-OK, got %+v", got)
	}
	wantCalls := len(cands) * (1 + retryTransientAttempts)
	if calls != wantCalls {
		t.Errorf("attempts: want %d total, got %d", wantCalls, calls)
	}
	if len(got.TryLog) != len(cands) {
		t.Errorf("tryLog: want %d entries, got %d", len(cands), len(got.TryLog))
	}
}

// TestRunnerClientClassBreaksRetry: ErrClient must NOT retry the same
// cand (4xx doesn't recover). Eval records calls == 1 per cand.
func TestRunnerClientClassBreaksRetry(t *testing.T) {
	r := router.New(mkRunnerTiers(), router.DefaultCooldownPolicy())
	runner := newRunner(r, nil)
	cands := r.Candidates("deepseek-v4-flash")
	if len(cands) == 0 {
		t.Fatal("setup: expected candidates")
	}
	calls := 0
	got := runner.Run(context.Background(), cands, func(ctx context.Context, cand router.Candidate, attempt int) evalResult {
		calls++
		return evalResult{Err: errors.New("model not found"), Class: router.ErrClient, Retryable: false}
	})
	if got.OK {
		t.Fatalf("want not-OK, got %+v", got)
	}
	// One attempt per cand, all three cands.
	if calls != len(cands) {
		t.Errorf("attempts: want %d (1 per cand), got %d", len(cands), calls)
	}
	if len(got.TryLog) != len(cands) {
		t.Errorf("tryLog: want %d entries, got %d", len(cands), len(got.TryLog))
	}
}

// TestRunnerEmittedStopsFailover: streaming eval reports Emitted=true on
// a non-success. Runner must stop immediately and not try the next
// candidate (would duplicate output).
func TestRunnerEmittedStopsFailover(t *testing.T) {
	r := router.New(mkRunnerTiers(), router.DefaultCooldownPolicy())
	runner := newRunner(r, nil)
	cands := r.Candidates("deepseek-v4-flash")
	if len(cands) < 2 {
		t.Fatal("setup: need 2+ candidates")
	}
	calls := 0
	got := runner.Run(context.Background(), cands, func(ctx context.Context, cand router.Candidate, attempt int) evalResult {
		calls++
		return evalResult{Err: errors.New("client gone"), Class: router.ErrClient, Emitted: true}
	})
	if got.OK {
		t.Fatalf("want not-OK, got %+v", got)
	}
	if calls != 1 {
		t.Errorf("attempts: want 1 (Emitted must stop), got %d", calls)
	}
	if len(got.TryLog) != 0 {
		t.Errorf("tryLog: want empty (no failover), got %d entries", len(got.TryLog))
	}
}

// TestRunnerAuthRefreshRetries: eval reports ErrAuth. refreshFn says
// "yes, I changed creds." Runner should retry the same cand without
// burning the retry budget. Next attempt succeeds.
func TestRunnerAuthRefreshRetries(t *testing.T) {
	r := router.New(mkRunnerTiers(), router.DefaultCooldownPolicy())
	refreshed := false
	refresh := func(env string) bool {
		refreshed = true
		return true
	}
	runner := newRunner(r, refresh)
	cands := r.Candidates("deepseek-v4-flash")
	if len(cands) == 0 {
		t.Fatal("setup: expected candidates")
	}
	calls := 0
	got := runner.Run(context.Background(), cands, func(ctx context.Context, cand router.Candidate, attempt int) evalResult {
		calls++
		if !refreshed {
			return evalResult{Err: errors.New("401"), Class: router.ErrAuth, Retryable: false}
		}
		return evalResult{OK: true, Response: &Response{StatusCode: 200}}
	})
	if !got.OK {
		t.Fatalf("want OK after refresh, got %+v", got)
	}
	if !refreshed {
		t.Error("refresh not called")
	}
	if calls != 2 {
		t.Errorf("attempts: want 2 (one + refresh retry), got %d", calls)
	}
	if len(got.TryLog) != 0 {
		t.Errorf("tryLog: want empty on success, got %d entries", len(got.TryLog))
	}
}

// TestRunnerAuthRefreshNoChange: ErrAuth + refresh returns false →
// runner treats as terminal, does not retry, moves to next cand.
func TestRunnerAuthRefreshNoChange(t *testing.T) {
	r := router.New(mkRunnerTiers(), router.DefaultCooldownPolicy())
	runner := newRunner(r, func(string) bool { return false })
	cands := r.Candidates("deepseek-v4-flash")
	calls := 0
	got := runner.Run(context.Background(), cands, func(ctx context.Context, cand router.Candidate, attempt int) evalResult {
		calls++
		return evalResult{Err: errors.New("401"), Class: router.ErrAuth, Retryable: false}
	})
	if got.OK {
		t.Fatalf("want not-OK, got %+v", got)
	}
	if calls != len(cands) {
		t.Errorf("attempts: want %d (1 per cand), got %d", len(cands), calls)
	}
	if len(got.TryLog) != len(cands) {
		t.Errorf("tryLog: want %d entries, got %d", len(cands), len(got.TryLog))
	}
}

// TestRunnerExhaustsAllCands: every cand fails. Runner returns
// TryLog with one entry per cand.
func TestRunnerExhaustsAllCands(t *testing.T) {
	r := router.New(mkRunnerTiers(), router.DefaultCooldownPolicy())
	runner := newRunner(r, nil)
	cands := r.Candidates("deepseek-v4-flash")
	calls := 0
	got := runner.Run(context.Background(), cands, func(ctx context.Context, cand router.Candidate, attempt int) evalResult {
		calls++
		return evalResult{Err: errors.New("5xx"), Class: router.ErrServer, Retryable: true}
	})
	if got.OK {
		t.Fatalf("want not-OK, got %+v", got)
	}
	if len(got.TryLog) != len(cands) {
		t.Errorf("tryLog: want %d entries, got %d", len(cands), len(got.TryLog))
	}
	if calls != len(cands)*(1+retryTransientAttempts) {
		t.Errorf("attempts: want %d, got %d", len(cands)*(1+retryTransientAttempts), calls)
	}
}

// TestBuildOutcome: unit test for the pure helper. Verifies the
// per-cand failure.Outcome shape (provider/kind/class/err/cooldown).
func TestBuildOutcome(t *testing.T) {
	r := router.New(mkRunnerTiers(), router.DefaultCooldownPolicy())
	cands := r.Candidates("deepseek-v4-flash")
	if len(cands) == 0 {
		t.Fatal("setup: expected candidates")
	}
	cand := cands[0]
	got := buildOutcome(cand, errors.New("boom"), router.ErrServer, r)
	if got.Provider != cand.Provider.Provider.Name {
		t.Errorf("Provider: want %q, got %q", cand.Provider.Provider.Name, got.Provider)
	}
	if got.Kind != cand.Provider.Provider.Kind {
		t.Errorf("Kind: want %q, got %q", cand.Provider.Provider.Kind, got.Kind)
	}
	if got.Class != router.ErrServer.String() {
		t.Errorf("Class: want %q, got %q", router.ErrServer.String(), got.Class)
	}
	if got.Err != "boom" {
		t.Errorf("Err: want %q, got %q", "boom", got.Err)
	}
}

// TestRunnerZeroAttempts: empty cands slice → OK=false, empty TryLog,
// no panic.
func TestRunnerZeroAttempts(t *testing.T) {
	r := router.New(mkRunnerTiers(), router.DefaultCooldownPolicy())
	runner := newRunner(r, nil)
	got := runner.Run(context.Background(), nil, func(ctx context.Context, cand router.Candidate, attempt int) evalResult {
		t.Fatal("eval should not be called with zero cands")
		return evalResult{}
	})
	if got.OK {
		t.Errorf("want not-OK, got %+v", got)
	}
	if len(got.TryLog) != 0 {
		t.Errorf("tryLog: want empty, got %d entries", len(got.TryLog))
	}
}

// TestRunnerContextCancel: ctx done before first attempt. Eval should
// still be called (eval decides what to do with ctx); the test
// exercises that the runner propagates context to eval correctly.
func TestRunnerContextCancel(t *testing.T) {
	r := router.New(mkRunnerTiers(), router.DefaultCooldownPolicy())
	runner := newRunner(r, nil)
	cands := r.Candidates("deepseek-v4-flash")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	got := runner.Run(ctx, cands, func(ctx context.Context, cand router.Candidate, attempt int) evalResult {
		calls++
		if ctx.Err() == nil {
			t.Error("eval: ctx not canceled")
		}
		return evalResult{Err: ctx.Err(), Class: router.ErrServer, Retryable: false}
	})
	if calls == 0 {
		t.Error("eval not called")
	}
	if got.OK {
		t.Errorf("want not-OK, got %+v", got)
	}
}

// TestRunnerRetryDelayNotZero: a real (non-zero) retry delay would
// slow tests. Confirm the runner honors an injected zero delay so
// future test-friendly construction doesn't need a mock clock.
func TestRunnerRetryDelayNotZero(t *testing.T) {
	r := router.New(mkRunnerTiers(), router.DefaultCooldownPolicy())
	runner := &candidateRunner{router: r, maxAttempt: 2, retryDelay: 0, refresh: nil}
	cands := r.Candidates("deepseek-v4-flash")
	start := time.Now()
	calls := 0
	got := runner.Run(context.Background(), cands, func(ctx context.Context, cand router.Candidate, attempt int) evalResult {
		calls++
		return evalResult{Err: errors.New("5xx"), Class: router.ErrServer, Retryable: true}
	})
	elapsed := time.Since(start)
	if got.OK {
		t.Fatalf("want not-OK, got %+v", got)
	}
	if calls != len(cands)*2 {
		t.Errorf("attempts: want %d, got %d", len(cands)*2, calls)
	}
	// 0-delay must keep total runtime well under the real delay. The
	// default 500ms x 1 retry x 3 cands = 1.5s; we cap at 200ms.
	if elapsed > 200*time.Millisecond {
		t.Errorf("runner slept too long: %v (zero-delay injection broken?)", elapsed)
	}
}
