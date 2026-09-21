package proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// stubTransport lets a test decide exactly when Do returns, so the
// header-timer race can be exercised deterministically instead of by luck.
type stubTransport struct {
	release chan struct{} // if non-nil, RoundTrip waits for it
	delay   time.Duration
	resp    *http.Response
}

func (s *stubTransport) RoundTrip(*http.Request) (*http.Response, error) {
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	if s.release != nil {
		<-s.release
	}
	return s.resp, nil
}

func stubResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// TestHeaderWaitClaimBeatsTimer mirrors
// TestFirstByteWatchdogNoCloseAfterFirstByte for the header watchdog: once the
// response has been claimed, a late timer callback must not cancel it (which
// would kill the body read out from under a live response).
func TestHeaderWaitClaimBeatsTimer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &headerWait{cancel: cancel}
	if !w.claim() {
		t.Fatal("claim must win when no timer has fired")
	}
	w.onTimeout() // the timer callback runs after the response was claimed
	if ctx.Err() != nil {
		t.Fatal("a late timer cancelled a claimed (live) response")
	}
}

// TestDoWithBudgetLiveResponseSurvivesLateTimer: the response wins the race,
// so waiting past the budget must leave its body readable.
func TestDoWithBudgetLiveResponseSurvivesLateTimer(t *testing.T) {
	h := &Handlers{HTTPClient: &http.Client{Transport: &stubTransport{resp: stubResponse("live")}}}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://stub.invalid/v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, release, err := h.doWithBudget(req, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("doWithBudget: %v", err)
	}
	defer release()
	// Well past the budget: the winner must have stopped the timer.
	time.Sleep(60 * time.Millisecond)
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("live body was killed by the header timer: %v", err)
	}
	if string(got) != "live" {
		t.Fatalf("body = %q, want %q", got, "live")
	}
}

// TestDoWithBudgetTimerWinsReturnsTimeout: the response arrives only AFTER the
// timer already fired. The context is dead, so the response must be reported
// as a timeout — not surfaced as a live 200 whose first read fails (the old
// behaviour, which for a stream meant a committed status followed by
// StreamAborted).
func TestDoWithBudgetTimerWinsReturnsTimeout(t *testing.T) {
	release := make(chan struct{})
	h := &Handlers{HTTPClient: &http.Client{Transport: &stubTransport{release: release, resp: stubResponse("late")}}}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://stub.invalid/v1", nil)
	if err != nil {
		t.Fatal(err)
	}

	type result struct {
		resp *http.Response
		err  error
	}
	done := make(chan result, 1)
	go func() {
		resp, _, err := h.doWithBudget(req, 20*time.Millisecond)
		done <- result{resp: resp, err: err}
	}()
	time.Sleep(60 * time.Millisecond) // let the timer win
	close(release)                    // ...then let Do return
	got := <-done

	if got.resp != nil {
		t.Fatal("a response whose context the timer killed must not be surfaced")
	}
	if !errors.Is(got.err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", got.err)
	}
}
