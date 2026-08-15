package router

import (
	"errors"
	"testing"
	"time"
)

func mkTiers() []TierInput {
	return []TierInput{
		{Name: "subscription", Providers: []ProviderInput{
			{Name: "a", Kind: "anthropic", BaseURL: "https://a", APIKeyEnv: "A", Models: []string{"m1"}},
		}},
		{Name: "cheap", Providers: []ProviderInput{
			{Name: "b", Kind: "openai", BaseURL: "https://b", APIKeyEnv: "B", Models: []string{"m2"}},
			{Name: "c", Kind: "openai", BaseURL: "https://c", APIKeyEnv: "C", Models: []string{"m3"}},
		}},
		{Name: "free", Providers: []ProviderInput{
			{Name: "d", Kind: "openai", BaseURL: "https://d", APIKeyEnv: "D", Models: []string{"m4"}},
		}},
	}
}

func TestTierOrder(t *testing.T) {
	r := New(mkTiers(), DefaultCooldownPolicy())
	if p := r.Next(0); p == nil || p.Provider.Name != "a" {
		t.Fatalf("first provider must be tier-1: %+v", p)
	}
	if p := r.Next(1); p == nil || p.Provider.Name != "b" {
		t.Fatalf("second provider must be tier-2 first: %+v", p)
	}
}

func TestCooldownSkipsProvider(t *testing.T) {
	r := New(mkTiers(), DefaultCooldownPolicy())
	a := r.Next(0)
	r.ReportFailure(a, ErrServer)
	if p := r.Next(0); p == nil || p.Provider.Name != "b" {
		t.Fatalf("provider in cooldown must be skipped: %+v", p)
	}
}

func TestCooldownExpiry(t *testing.T) {
	r := New(mkTiers(), CooldownPolicy{Base: time.Millisecond, Max: time.Minute, MaxHits: 10})
	a := r.Next(0)
	r.ReportFailure(a, ErrServer)
	time.Sleep(5 * time.Millisecond)
	if p := r.Next(0); p == nil || p.Provider.Name != "a" {
		t.Fatal("provider must recover after cooldown")
	}
}

func TestExponentialBackoff(t *testing.T) {
	r := New(mkTiers(), DefaultCooldownPolicy())
	a := r.Next(0)
	r.ReportFailure(a, ErrServer)
	first := r.CooldownRemaining(a)
	r.ReportFailure(a, ErrServer)
	second := r.CooldownRemaining(a)
	if second <= first {
		t.Fatalf("backoff must grow: %v then %v", first, second)
	}
}

func TestBackoffCapped(t *testing.T) {
	r := New(mkTiers(), CooldownPolicy{Base: time.Second, Max: time.Minute, MaxHits: 100})
	a := r.Next(0)
	for i := 0; i < 20; i++ {
		r.ReportFailure(a, ErrServer)
	}
	if got := r.CooldownRemaining(a); got > time.Minute {
		t.Fatalf("backoff must cap at Max: %v", got)
	}
}

func TestSuccessResets(t *testing.T) {
	r := New(mkTiers(), DefaultCooldownPolicy())
	a := r.Next(0)
	r.ReportFailure(a, ErrServer)
	r.ReportSuccess(a)
	if got := r.CooldownRemaining(a); got != 0 {
		t.Fatalf("success must clear cooldown: %v", got)
	}
}

func TestStreamAbortDoesNotEscalate(t *testing.T) {
	r := New(mkTiers(), DefaultCooldownPolicy())
	a := r.Next(0)
	r.ReportFailure(a, ErrStream)
	if got := r.CooldownRemaining(a); got != 0 {
		t.Fatal("stream abort must not escalate cooldown")
	}
}

func TestRetryableClasses(t *testing.T) {
	for _, c := range []ErrClass{ErrNetwork, ErrTimeout, ErrRateLimit, ErrAuth, ErrServer} {
		if !IsRetryableClass(c) {
			t.Fatalf("%v must be retryable", c)
		}
	}
	for _, c := range []ErrClass{ErrClient, ErrStream} {
		if IsRetryableClass(c) {
			t.Fatalf("%v must NOT be retryable", c)
		}
	}
}

func TestClassifyStatus(t *testing.T) {
	cases := map[int]ErrClass{401: ErrAuth, 403: ErrAuth, 429: ErrRateLimit, 500: ErrServer, 502: ErrServer, 400: ErrClient, 404: ErrClient, 200: ErrClient}
	for status, want := range cases {
		if got := ClassifyStatus(status); got != want {
			t.Errorf("ClassifyStatus(%d) = %v, want %v", status, got, want)
		}
	}
}

func TestClassifyError(t *testing.T) {
	if Classify(StreamAborted()) != ErrStream {
		t.Fatal("stream abort must classify as ErrStream")
	}
	if Classify(errors.New("dial tcp: connection refused")) != ErrNetwork {
		t.Fatal("network error must classify as ErrNetwork")
	}
	if Classify(contextDeadlineExceeded) != ErrTimeout {
		t.Fatal("deadline error must classify as ErrTimeout")
	}
}

func TestAllExhausted(t *testing.T) {
	r := New(mkTiers(), DefaultCooldownPolicy())
	a := r.Next(0)
	r.ReportFailure(a, ErrServer)
	b := r.Next(1)
	r.ReportFailure(b, ErrServer)
	c := r.Next(2)
	r.ReportFailure(c, ErrServer)
	d := r.Next(3)
	r.ReportFailure(d, ErrServer)
	if p := r.Next(0); p != nil {
		t.Fatalf("all providers in cooldown: Next must return nil, got %+v", p)
	}
}
