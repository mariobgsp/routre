package proxy

import (
	"strings"
	"testing"
)

func TestSanitizeDropsReasoningAndPrevID(t *testing.T) {
	in := `{"model":"m","previous_response_id":"r1","input":[{"type":"reasoning","encrypted_content":"x"},{"type":"message","role":"user","content":"hi"}]}`
	out := string(sanitizeResponsesPayload([]byte(in)))
	if strings.Contains(out, "reasoning") || strings.Contains(out, "encrypted_content") || strings.Contains(out, "previous_response_id") {
		t.Fatalf("sanitize must drop caller-bound state: %s", out)
	}
	if !strings.Contains(out, `"content":"hi"`) {
		t.Fatalf("sanitize must keep user content: %s", out)
	}
}

func TestResponsesCacheable(t *testing.T) {
	if responsesCacheable([]byte(`{"input":[{"type":"reasoning"}]}`)) {
		t.Fatal("reasoning bodies must not be cacheable")
	}
	if responsesCacheable([]byte(`{"input":[{"type" : "reasoning"}]}`)) {
		t.Fatal("spaced reasoning bodies must not be cacheable")
	}
	if !responsesCacheable([]byte(`{"model":"m","input":"hi"}`)) {
		t.Fatal("safe bodies must be cacheable")
	}
}

func TestSanitizeKeepsPrevIDStripOnOddInput(t *testing.T) {
	in := `{"model":"m","previous_response_id":"r1","input":null}`
	out := string(sanitizeResponsesPayload([]byte(in)))
	if strings.Contains(out, "previous_response_id") {
		t.Fatalf("prev id must be stripped even on odd input: %s", out)
	}
}

func TestIsReasoningStateError(t *testing.T) {
	if !isReasoningStateError(400, []byte(`reasoning encrypted_content was not issued to this caller`)) {
		t.Fatal("must match upstream reasoning rejection")
	}
	if isReasoningStateError(500, []byte(`reasoning encrypted_content was not issued`)) {
		t.Fatal("only 400 qualifies")
	}
}
