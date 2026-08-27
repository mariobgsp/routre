package proxy

import (
	"context"
	"time"

	"github.com/mariobgsp/routre/internal/proxy/failures"
	"github.com/mariobgsp/routre/internal/router"
)

// Phases records the wall-clock checkpoints for one upstream attempt.
// Latency survey #4: per-phase observability is the foundation for
// diagnosing TTFB regressions. The fields are populated by eval, not
// by the runner; the runner just passes them through. Streaming
// eval populates all three; non-streaming eval sets Total only
// (dial + headers + first body all complete before returning).
type Phases struct {
	DialMS    int64 // time from start to first byte of HTTP request on the wire
	HeadersMS int64 // time to receive response headers (Do returns)
	TTFBMS    int64 // time to first body byte (streaming only)
	TotalMS   int64 // time for the whole attempt (success or failure)
}

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
	// Phases is the per-attempt wall-clock breakdown. Streaming eval
	// populates DialMS/HeadersMS/TTFBMS/TotalMS; non-streaming sets
	// only TotalMS (the three earlier checkpoints all collapse into
	// the one `relay` call). nil-safe; runners and callers should
	// treat nil as "not measured."
	Phases *Phases
}

// evalFn runs a single attempt against a candidate. The runner invokes
// it once per retry, with attempt starting at 0 and the current cand
// passed in. eval is responsible for building the payload
// (translate/clamp/prompt-cache) and for all post-success side effects
// (usage, metrics, cache write); the runner only owns iteration,
// retry, refresh, and tryLog accumulation.
type evalFn func(ctx context.Context, cand router.Candidate, attempt int) evalResult

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
	maxAttempt int           // total attempts per candidate (1 + retryTransientAttempts)
	retryDelay time.Duration // pause between retries of the same candidate
	refresh    refreshFn     // nil disables the auth-refresh-and-retry path
}

// newRunner builds a runner with the project's default policy. Tests
// can construct one directly with custom retry knobs.
func newRunner(r *router.Router, refresh refreshFn) *candidateRunner {
	return &candidateRunner{
		router:     r,
		maxAttempt: 1 + retryTransientAttempts,
		retryDelay: transientRetryDelay,
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
	Phases   *Phases
}

// Run iterates over cands, invoking eval once per attempt per candidate.
// On the first success it returns. On exhaustion, it returns the
// accumulated TryLog (one entry per attempted candidate) so the caller
// can render an "all providers failed" response.
func (r *candidateRunner) Run(ctx context.Context, cands []router.Candidate, eval evalFn) runnerResult {
	tryLog := make([]failures.Outcome, 0, len(cands))
	for _, cand := range cands {
		var (
			lastErr   error
			lastClass router.ErrClass
			appended  bool // record this cand's outcome the first time we leave the inner loop on a failure
		)
		for attempt := 0; attempt < r.maxAttempt; attempt++ {
			if attempt > 0 {
				if lastClass == router.ErrClient {
					// 4xx-class failures never recover with a same-cand retry.
					break
				}
				time.Sleep(r.retryDelay)
			}
			res := eval(ctx, cand, attempt)
			// OK means "stop iterating candidates" — either a real
			// success (Err == nil) or a streaming eval that already
			// committed bytes (Err != nil, Emitted == true). In both
			// cases we must not failover.
			if res.OK {
				return runnerResult{OK: res.Err == nil, Response: res.Response, TryLog: tryLog, Phases: res.Phases}
			}
			// Once a streaming eval emitted bytes, failover would
			// duplicate output. Stop trying immediately. The current
			// cand is not added to the tryLog because the client
			// already has its bytes.
			if res.Emitted {
				return runnerResult{OK: false, TryLog: tryLog}
			}
			if res.Err != nil {
				lastErr = res.Err
				lastClass = res.Class
			}
			// Auth-refresh-and-retry: on ErrAuth with a successful
			// credential refresh, loop immediately (no sleep, no
			// attempt budget consumed for the refresh itself — the
			// refresh attempt counts as the current `attempt`).
			if res.Class == router.ErrAuth && r.refresh != nil {
				if r.refresh(cand.Provider.Provider.APIKeyEnv) {
					continue
				}
			}
			// Terminal failure for this cand: record it once so a
			// later success carries the failure history. Retryable
			// failures retry the same cand and don't record yet.
			if !res.Retryable || !router.IsRetryableClass(res.Class) {
				if !appended {
					tryLog = append(tryLog, buildOutcome(cand, lastErr, lastClass, r.router))
					appended = true
				}
				break
			}
		}
		// Inner loop exhausted via retry budget without a terminal
		// signal (every attempt was retryable but the budget ran
		// out). Record now.
		if !appended {
			tryLog = append(tryLog, buildOutcome(cand, lastErr, lastClass, r.router))
		}
	}
	return runnerResult{OK: false, TryLog: tryLog}
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
