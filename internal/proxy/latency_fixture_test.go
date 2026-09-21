package proxy

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
)

// nextFixtureNonce hands out a process-unique nonce. tokenize.Count memoises
// by hash of the whole string, so a nonce that restarts per iteration or per
// sub-benchmark would let a warm count mask the cold request path.
var fixtureNonce atomic.Int64

func nextFixtureNonce() int { return int(fixtureNonce.Add(1)) }

// fixtureToolHeavyBody builds a valid OpenAI chat request whose payload is
// dominated by tool_result content, so RTK's filters fire and compression
// actually runs. The nonce makes every call unique: tokenize.Count's LRU
// stays cold and the exact-match cache misses, so the measurement is the
// request path rather than a warm shortcut.
//
// The returned body is approximately size bytes (within one content block).
func fixtureToolHeavyBody(t testing.TB, size int, nonce int) []byte {
	t.Helper()
	var sb strings.Builder
	sb.WriteString(`{"model":"m","max_tokens":4096,"messages":[`)
	for seed := 0; sb.Len() < size; seed++ {
		if seed > 0 {
			sb.WriteByte(',')
		}
		content, err := json.Marshal(grepShapedContent(seed, nonce))
		if err != nil {
			t.Fatalf("marshal tool content: %v", err)
		}
		fmt.Fprintf(&sb, `{"role":"tool","tool_call_id":"call_%d","content":%s}`, seed, content)
	}
	sb.WriteString(`]}`)
	body := []byte(sb.String())
	if !json.Valid(body) {
		t.Fatalf("fixture body is not valid JSON (%d bytes)", len(body))
	}
	return body
}

// grepShapedContent returns ~4 KiB of `path:line:` output, the shape RTK's
// grep filter scores highest, so the truncate/dedup pass genuinely shrinks
// the payload.
func grepShapedContent(seed, nonce int) string {
	var sb strings.Builder
	for i := 0; sb.Len() < 4096; i++ {
		fmt.Fprintf(&sb, "internal/proxy/fixture%04d.go:%d:func handler%04d(ctx context.Context) error { // nonce=%d\n",
			i%97, seed*1000+i, seed*1000+i, nonce)
	}
	return sb.String()
}
