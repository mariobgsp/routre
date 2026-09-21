package proxy

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mariobgsp/routre/internal/router"
)

// TestRunnerZeroAttemptCandidateIsNotReportedAsNetworkFailure: when a
// candidate's slice is already spent there is no attempt at all, so it must
// not be reported as a network failure. buildOutcome's zero-value class is
// ErrNetwork, and the pre-fix runner appended it with a nil error — blaming a
// provider that was never called (`class:"network"`, `err:""`).
func TestRunnerZeroAttemptCandidateIsNotReportedAsNetworkFailure(t *testing.T) {
	// A 1ns candidate slice with a 1ms request budget: the outer deadline check
	// passes, but time has advanced by the time the inner check runs, so every
	// candidate breaks before eval.
	setBudgets(t, time.Nanosecond, time.Millisecond)
	r := router.New(mkRunnerTiers(), router.DefaultCooldownPolicy())
	runner := newRunner(r, nil)
	cands := r.Candidates("deepseek-v4-flash")
	if len(cands) < 2 {
		t.Fatal("setup: need 2 candidates")
	}

	calls := 0
	got := runner.Run(context.Background(), cands[:2], func(context.Context, router.Candidate, int, time.Duration) evalResult {
		calls++
		return evalResult{Err: errors.New("boom"), Class: router.ErrServer, Retryable: false}
	})

	if calls != 0 {
		t.Fatalf("eval ran %d times, want 0 (the slice must be spent before any attempt)", calls)
	}
	if !got.BudgetExhausted {
		t.Fatalf("want BudgetExhausted, got %+v", got)
	}
	if got.Untried != 2 {
		t.Fatalf("Untried = %d, want 2", got.Untried)
	}
	for _, o := range got.TryLog {
		if o.Class == router.ErrNetwork.String() {
			t.Fatalf("provider %s was reported as a network failure without being called (err=%q)", o.Provider, o.Err)
		}
	}
	// The effective budget must be reported, not the package var re-read later.
	if got.Budget != time.Millisecond {
		t.Fatalf("Budget = %v, want the effective 1ms", got.Budget)
	}
}
