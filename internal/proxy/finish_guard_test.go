package proxy

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestRelayOpenAIGuaranteeFinish_injectsWhenMissing(t *testing.T) {
	// gpt-5.6-luna-style stream: content chunks, then plain [DONE], no finish_reason.
	up := strings.NewReader(
		"data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n" +
			"data: [DONE]\n\n")
	var buf bytes.Buffer
	if err := relayOpenAIGuaranteeFinish(&buf, up, nil); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, `"finish_reason":"stop"`) {
		t.Fatalf("finish_reason not injected:\n%s", got)
	}
	// content chunk must pass through untouched
	if !strings.Contains(got, `"content":"hi"`) {
		t.Fatalf("content altered:\n%s", got)
	}
}

func TestRelayOpenAIGuaranteeFinish_passthroughWhenPresent(t *testing.T) {
	up := strings.NewReader(
		"data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\n" +
			"data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
			"data: [DONE]\n\n")
	var buf bytes.Buffer
	if err := relayOpenAIGuaranteeFinish(&buf, up, nil); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	got := buf.String()
	if n := strings.Count(got, `"finish_reason":"stop"`); n != 1 {
		t.Fatalf("expected 1 finish_reason, got %d:\n%s", n, got)
	}
}

func TestRelayOpenAIGuaranteeFinish_abruptEOF(t *testing.T) {
	// Upstream closes with no [DONE] and no finish_reason (the gpt-5.6-luna case).
	up := strings.NewReader("data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"yo\"},\"finish_reason\":null}]}\n\n")
	var buf bytes.Buffer
	if err := relayOpenAIGuaranteeFinish(&buf, up, nil); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(buf.String(), `"finish_reason":"stop"`) {
		t.Fatalf("finish_reason not injected on abrupt EOF:\n%s", buf.String())
	}
}

var _ io.Reader // silence unused import if a test is pruned
