package router

import "testing"

// TestClassifyLiveRetryStormBodies is a regression test for a real incident:
// the gateway's default client model ("muse-spark-*-contributor-free") was
// not served by any configured provider. Each provider returned a hard
// rejection whose body names the model, yet a 401-shaped one ("Model X is
// not supported", model name between the words) stayed classified ErrAuth,
// so every request replayed the full auth-refresh/retry cascade across all
// providers (observed 3 providers x 3 attempts of wall-clock timeouts).
// All three bodies below must classify ErrClient (non-retryable failover).
func TestClassifyLiveRetryStormBodies(t *testing.T) {
	bodies := []struct{ status int; body string }{
		{400, `{"error":{"message":"Model \"muse-spark-1.3-contributor-free\" is not supported on this endpoint.","type":"invalid_request_error","param":"model","code":"unsupported_model"}}`},
		{401, `{"type":"error","error":{"type":"ModelError","message":"Model muse-spark-1.3-contributor-free is not supported"}}`},
		{400, `{"error":{"message":"muse-spark-1.3-contributor-free is not a valid model ID","code":400}}`},
	}
	for i, b := range bodies {
		if got := ClassifyStatusBody(b.status, []byte(b.body)); got != ErrClient {
			t.Errorf("case %d (status %d): got %v, want ErrClient — retry storm would replay", i, b.status, got)
		}
	}
}
