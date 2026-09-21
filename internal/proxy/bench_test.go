package proxy

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/mariobgsp/routre/internal/mock"
)

// latencyAssertEnv turns the <10 ms p99 print into an assertion on a
// deliberate run. CI prints and passes (benchmarks are inherently noisy).
const latencyAssertEnv = "ROUTRE_ASSERT_LATENCY"

// latencyBudgetMS is the gateway-added p99 budget for a 1 MiB body. The
// 10 MiB benchmark is reported only: the goal's size envelope is 1 MiB.
const latencyBudgetMS = 10.0

func BenchmarkGatewayAddedLatency1MB(b *testing.B)  { benchmarkGatewayAddedLatency(b, 1<<20) }
func BenchmarkGatewayAddedLatency10MB(b *testing.B) { benchmarkGatewayAddedLatency(b, 10<<20) }

// BenchmarkEnvelopePasses breaks the 1 MiB request into pipeline-stage
// configurations so each later commit has a before/after instrument.
func BenchmarkEnvelopePasses(b *testing.B) {
	for _, tc := range []struct {
		name  string
		rtk   bool
		cache bool
	}{
		{"rtk+cache", true, true},
		{"rtk-only", true, false},
		{"cache-only", false, true},
		{"both-off", false, false},
	} {
		b.Run(tc.name, func(b *testing.B) {
			benchmarkGatewayAddedLatencyConfig(b, 1<<20, tc.rtk, tc.cache)
		})
	}
}

func benchmarkGatewayAddedLatency(b *testing.B, size int) {
	benchmarkGatewayAddedLatencyConfig(b, size, true, true)
}

// benchmarkGatewayAddedLatencyConfig measures the client-observed duration
// of one request through a real HTTP server against an instant mock upstream.
// The mock's service time is ~0.1 ms, so the measurement is gateway-added
// latency plus loopback. Fixture construction is deliberately OUTSIDE the
// timed window (it is harness cost, not gateway cost).
func benchmarkGatewayAddedLatencyConfig(b *testing.B, size int, rtkOn, cacheOn bool) {
	b.Helper()
	up, err := mock.New("bench")
	if err != nil {
		b.Fatalf("mock upstream: %v", err)
	}
	b.Cleanup(up.Close)
	b.Setenv("TEST_KEY_A", "test-key-a")
	cfg := fmt.Sprintf(`{"listen":"127.0.0.1:0","tiers":[{"name":"t","providers":[{"name":"a","kind":"openai","base_url":"%s/v1","api_key_env":"TEST_KEY_A","models":["m"]}]}],"rtk":{"enabled":%v,"min_bytes":500,"max_bytes":10485760},"cache":{"enabled":%v,"max_entries":4096,"ttl_seconds":3600,"prefix_order":true,"canonical_keys":true}}`,
		up.URL(), rtkOn, cacheOn)
	st := loadTestStore(b, cfg)
	base, _, _ := serveGateway(b, st)

	client := &http.Client{Timeout: 2 * time.Minute}
	url := base + "/v1/chat/completions"
	durs := make([]time.Duration, 0, b.N)
	bodySize := 0

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		body := fixtureToolHeavyBody(b, size, nextFixtureNonce()) // untimed harness cost
		bodySize = len(body)
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			b.Fatalf("new request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		start := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			b.Fatalf("request: %v", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		elapsed := time.Since(start)
		if resp.StatusCode != http.StatusOK {
			b.Fatalf("status %d for a %d-byte body", resp.StatusCode, len(body))
		}
		durs = append(durs, elapsed)
	}
	b.StopTimer()
	reportLatency(b, durs, size, bodySize, rtkOn, cacheOn)
}

// reportLatency prints p50/p95/p99 and asserts only on a deliberate run
// (ROUTRE_ASSERT_LATENCY=1) and only inside the goal's 1 MiB envelope.
func reportLatency(b *testing.B, durs []time.Duration, targetSize, bodySize int, rtkOn, cacheOn bool) {
	b.Helper()
	if len(durs) == 0 {
		b.Fatal("no samples")
	}
	sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })
	quantile := func(q float64) time.Duration {
		idx := int(q * float64(len(durs)-1))
		if idx < 0 {
			idx = 0
		}
		return durs[idx]
	}
	ms := func(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
	p50, p95, p99 := quantile(0.50), quantile(0.95), quantile(0.99)
	b.ReportMetric(ms(p50), "p50_ms")
	b.ReportMetric(ms(p95), "p95_ms")
	b.ReportMetric(ms(p99), "p99_ms")
	b.Logf("envelope=%dMiB body=%dB rtk=%v cache=%v n=%d p50=%.2fms p95=%.2fms p99=%.2fms",
		targetSize>>20, bodySize, rtkOn, cacheOn, len(durs), ms(p50), ms(p95), ms(p99))
	if os.Getenv(latencyAssertEnv) == "1" && targetSize <= 1<<20 && ms(p99) >= latencyBudgetMS {
		b.Fatalf("gateway-added p99 %.2f ms >= %.0f ms budget at %d MiB (body %d B)",
			ms(p99), latencyBudgetMS, targetSize>>20, bodySize)
	}
}
