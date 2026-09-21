package router

import (
	"errors"
	"fmt"
	"testing"
)

// TestMissingProviderKeyClassifiesAsConfig pins the sentinel mapping: a
// provider key env var that is not set is a gateway config error, so it must
// never cooldown a provider or consume a same-candidate retry.
func TestMissingProviderKeyClassifiesAsConfig(t *testing.T) {
	err := fmt.Errorf("provider key X is not set (use `routre setup` or export it): %w", ErrMissingProviderKey)
	if got := Classify(err); got != ErrConfig {
		t.Fatalf("Classify(missing key) = %v, want ErrConfig", got)
	}
	if IsRetryableClass(ErrConfig) {
		t.Fatal("ErrConfig must not be retryable")
	}
	if got := ErrConfig.String(); got != "config" {
		t.Fatalf("ErrConfig.String() = %q, want %q", got, "config")
	}
	// A genuine transport error stays a network error (retryable).
	if got := Classify(errors.New("connection refused")); got != ErrNetwork {
		t.Fatalf("Classify(network) = %v, want ErrNetwork", got)
	}
}
