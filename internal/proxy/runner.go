package proxy

import (
	"context"
	"time"

	"github.com/mariobgsp/routre/internal/proxy/failures"
	"github.com/mariobgsp/routre/internal/router"
)

// Phases moved to phases.go (shared with pipeline).

// evalResult is the per-attempt outcome returned by an eval callback to the
// candidateRunner. The runner only needs to know: did this attempt succeed?
// If not, what's the error/class, and was anything already emitted to the
// client (streaming only)?
type evalResult struct {
	// OK means "stop iterating candidates" — eval handled the failure in a
	// way that doesn't admit failover (e.g. a non-retryable class, or the
	// caller decided to return a custom response). When OK is true and
	// Err is nil, the attempt succeeded.
	OK bool
	// Err is the error from this attempt, if any. nil on success.
	Err error
	// Class is the error classification. The runner uses this to decide
	// retry vs failover.
	Class router.ErrClass
	// Response is the non-streaming response on success. The runner
	// returns the first successful response to its caller. Streaming
	// callbacks leave this nil (they write directly to the ResponseWriter).
	Response *Response
	// Retryable reports whether this attempt's failure admits a same-cand
	// retry. The runner only retries when Retryable is true. Defaults to
	// false if eval leaves it unset; streaming eval should set this to
	// match the old tryLog behavior (transient classes retry, client
	// classes do not).
	Retryable bool
	// Emitted is the streaming pre-first-byte contract: true if eval
	// already wrote bytes to the ResponseWriter. Once Emitted is true,
	// the runner must not failover — the client has already received
	// output and a second attempt would duplicate it. Always false for
	// non-streaming eval.
	Emitted bool
	// Written reports a non-2xx response a streaming eval has already
	// committed to the client (e.g. a deterministic upstream 400
	// surfaced verbatim). Emitted=true with Written=nil is the mid-stream
	// abort (partial 200 — nothing written by the failure path); the
	// caller uses Written to record the real status instead of assuming
	// success, and must never write a second body.
	Written *streamWritten
	// Phases is the per-attempt wall-clock breakdown. Streaming eval
	// populates DialMS/HeadersMS/TTFBMS/TotalMS; non-streaming sets
	// only TotalMS (the three earlier checkpoints all collapse into
	// the one `relay` call). nil-safe; runners and callers should
	// treat nil as "not measured."
	Phases *Phases
}

// evalFn runs a single attempt against a candidate. The runner invokes
// it once per retry, with attempt starting at 0, the current cand, and the
// wall-clock budget this attempt may use (never more than the candidate's
// fair slice of the remaining request budget). eval is responsible for building the payload
// (translate/clamp/prompt-cache) and for all post-success side effects
// (usage, metrics, cache write); the runner only owns iteration,
// retry, refresh, and tryLog accumulation.
type evalFn func(ctx context.Context, cand router.Candidate, attempt int, budget time.Duration) evalResult

// refreshFn returns true if it actually changed credentials, signaling
// the runner to retry the same candidate. Routre injects this from
// Handlers.refreshCredentials; tests inject a stub.
type refreshFn func(apiKeyEnv string) bool

// candidateRunner encapsulates the per-candidate retry policy: the
// transient-retry loop, the auth-refresh-and-retry path, and the
// per-candidate failure.Outcome entry. It is a deep module: the
// Run signature is small, the hidden behavior is large, and the same
// policy is shared between non-streaming (processInternal) and streaming
// (Stream) call sites.
type candidateRunner struct {
	router     *router.Router
	maxAttempt int       // total attempts per candidate (1 + retryTransientAttempts)
	refresh    refreshFn // nil disables the auth-refresh-and-retry path
}

// FailoverBudget bounds how long the runner spends CHOOSING a candidate. It
// never cancels an attempt already in flight (the generation backstop does
// that). Vars rather than consts only so the budget tests can shrink them;
// TestFailoverBudgetsAreHardcoded pins the shipped values. Deliberately not
// config keys.
var (
	candidateFailoverBudget = 15 * time.Second // per candidate, pre-first-byte
	requestFailoverBudget   = 30 * time.Second // whole request, across candidates
)

// newRunner builds a runner with the project's default policy. Tests
// can construct one directly with custom retry knobs.
func newRunner(r *router.Router, refresh refreshFn) *candidateRunner {
	return &candidateRunner{
		router:     r,
		maxAttempt: 1 + retryTransientAttempts,
		refresh:    refresh,
	}
}

// runnerResult is the per-call return value. Streaming callers check
// only OK; non-streaming callers also read Response. Phases carries
// the per-attempt timing from the successful eval (nil if all
// candidates failed or eval didn't measure).
type runnerResult struct {
	Response *Response
	TryLog   []failures.Outcome
	OK       bool
	// Emitted is true when the runner stopped because a streaming attempt
	// already committed bytes to the client (mid-stream abort / committed
	// non-retryable failure). The caller must not render an all-failed
	// response on top of the committed output.
	Emitted bool
	// Written carries the status of an already-committed non-2xx stream
	// response (nil when nothing was written or the stream succeeded).
	Written *streamWritten
	Phases  *Phases
	// BudgetExhausted is true when the request budget ran out before some
	// candidate was attempted. Untried is how many were never tried; the
	// caller renders a failover_budget outcome so the 503 never claims a
	// provider failed when it was never asked.
	BudgetExhausted bool
	Untried         int
	// Budget is the request budget this run actually used, so callers report
	// the effective value rather than re-reading the (mutable) package var.
	Budget time.Duration
}

// Run iterates over cands, invoking eval once per attempt per candidate.
// On the first success it returns. On exhaustion, it returns the
// accumulated TryLog (one entry per attempted candidate) so the caller
// can render an "all providers failed" response.
//
// Each candidate is allocated a fair slice of the remaining request budget,
// so every candidate that has not been tried yet is guaranteed a window: a
// same-candidate retry can never consume the share a healthy untried
// candidate still needs.
func (r *candidateRunner) Run(ctx context.Context, cands []router.Candidate, eval evalFn) runnerResult {
	tryLog := make([]failures.Outcome, 0, len(cands))
	requestBudget := requestFailoverBudget
	deadline := time.Now().Add(requestBudget)
	untried := len(cands)
	skipped := 0
	for i, cand := range cands {
		if time.Until(deadline) <= 0 {
			skipped = len(cands) - i
			break
		}
		slice := time.Until(deadline) / time.Duration(untried)
		if slice > candidateFailoverBudget {
			slice = candidateFailoverBudget
		}
		candDeadline := time.Now().Add(slice)
		untried--

		var (
			lastErr   error
			lastClass router.ErrClass
			appended  bool
			attempted bool
		)
		for attempt := 0; attempt < r.maxAttempt; attempt++ {
			budget := time.Until(candDeadline)
			if budget <= 0 {
				break
			}
			attempted = true
			res := eval(ctx, cand, attempt, budget)
			// OK means "stop iterating candidates" — either a real
			// success (Err == nil) or a streaming eval that already
			// committed bytes (Err != nil, Emitted == true). In both
			// cases we must not failover.
			if res.OK {
				return runnerResult{OK: res.Err == nil, Emitted: res.Emitted, Response: res.Response, TryLog: tryLog, Written: res.Written, Phases: res.Phases}
			}
			// Once a streaming eval emitted bytes, failover would
			// duplicate output. Stop immediately.
			if res.Emitted {
				return runnerResult{OK: false, TryLog: tryLog, Written: res.Written}
			}
			if res.Err != nil {
				lastErr = res.Err
				lastClass = res.Class
			}
			// Auth-refresh-and-retry: on ErrAuth with a successful
			// credential refresh, loop immediately (no sleep).
			if res.Class == router.ErrAuth && r.refresh != nil {
				if r.refresh(cand.Provider.Provider.APIKeyEnv) {
					continue
				}
			}
			// A same-candidate retry is only for connection-level errors
			// (dial refused/reset, no route): an immediate identical retry
			// cannot change a 5xx/429/overloaded answer, so those fail over
			// instead of burning another candidate's window.
			if attempt+1 < r.maxAttempt && res.Retryable && res.Class == router.ErrNetwork {
				continue
			}
			if !appended {
				tryLog = append(tryLog, buildOutcome(cand, lastErr, lastClass, r.router))
				appended = true
			}
			break
		}
		if !appended {
			if attempted {
				tryLog = append(tryLog, buildOutcome(cand, lastErr, lastClass, r.router))
			} else {
				// The candidate's slice was already spent when its turn came, so
				// no attempt ran. Reporting it via buildOutcome would send
				// lastClass at its zero value (ErrNetwork) with a nil error —
				// blaming a provider that was never called. Count it as untried
				// instead; the caller renders the honest failover_budget entry.
				skipped++
			}
		}
	}
	return runnerResult{OK: false, TryLog: tryLog, BudgetExhausted: skipped > 0, Untried: skipped, Budget: requestBudget}
}

// buildOutcome converts a candidate + last error/class into the
// failures.Outcome shape the wire expects. Pulled out so both call
// sites (processInternal, Stream) and the runner itself produce
// identical entries without copy-paste.
func buildOutcome(cand router.Candidate, lastErr error, lastClass router.ErrClass, r *router.Router) failures.Outcome {
	entry := failures.Outcome{
		Provider: cand.Provider.Provider.Name,
		Kind:     cand.Provider.Provider.Kind,
		Class:    lastClass.String(),
	}
	if lastErr != nil {
		entry.Err = lastErr.Error()
	}
	if r != nil {
		if cd := r.CooldownRemaining(cand.Provider); cd > 0 {
			entry.Cooldown = cd
		}
	}
	return entry
}

// retryTransientAttempts: how many times a candidate is retried on a
// connection-level failure (dial refused/reset, no route) before failover
// moves on. Upstream 5xx/429/overloaded responses are NOT retried on the same
// candidate: an immediate identical retry cannot change the answer, so they
// fail over instead. No sleep is involved.
const retryTransientAttempts = 1
