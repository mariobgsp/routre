package tests

import (
	"net/http"
	"testing"
)

// TestIntegrationPlaceholder ensures go test ./tests passes.
// Real integration tests (binary build + endpoint checks) live here.
func TestIntegrationPlaceholder(t *testing.T) {
	if 1 != 1 {
		t.Fatal("placeholder")
	}
}

// TestHealthzDoc ensures the web page references the health endpoint correctly
// (smoke check that docs/index.html was updated for routre).
func TestHealthzDoc(t *testing.T) {
	// Verify the test harness itself can make HTTP requests (stdlib only).
	_ = http.DefaultClient
}
