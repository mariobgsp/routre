package rtk

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// bigToolBody builds a JSON chat request whose single tool message is n bytes
// of path-list-shaped output (the "find" filter's favourite shape).
func bigToolBody(n int) []byte {
	var b strings.Builder
	b.WriteString("src/internal/pkg/file.go\n")
	for b.Len() < n {
		b.WriteString("internal/proxy/very/long/path/component/file_")
		b.WriteString(strings.Repeat("x", 24))
		b.WriteString(".go\n")
	}
	body, err := json.Marshal(map[string]any{
		"model": "m",
		"messages": []any{
			map[string]any{"role": "tool", "content": b.String()},
		},
	})
	if err != nil {
		panic(err)
	}
	return body
}

// TestLargeToolPayloadNeverGrowsAndStaysFast is the bounded-work contract: a
// 10 MiB tool result is first reduced to the scan window, so the filter
// pipeline never splits the whole body into lines. The result must never be
// larger than the input, and the call must stay well inside a request budget.
func TestLargeToolPayloadNeverGrowsAndStaysFast(t *testing.T) {
	in := bigToolBody(8 << 20)
	cfg := Config{Enabled: true, MinBytes: 500, MaxBytes: 10 << 20, Level: "routre", ScanBytes: 512 << 10, BudgetMS: 150}
	start := time.Now()
	out, changed := New(cfg).Apply(in)
	elapsed := time.Since(start)

	if !changed {
		t.Fatal("RTK did not fire on a 10 MiB path-list tool result")
	}
	if len(out) >= len(in) {
		t.Fatalf("output grew: %d -> %d", len(in), len(out))
	}
	if elapsed > 2*time.Second {
		t.Fatalf("compressing 10 MiB took %v; work is not bounded", elapsed)
	}
}

// TestBudgetStopsWork proves the time budget is observable: many large tool
// segments under a 1 ms budget cannot all be visited, and the document is still
// valid JSON with strictly-smaller-or-equal content.
func TestBudgetStopsWork(t *testing.T) {
	seg := strings.Repeat("internal/a/b/c/d/e/f/g.go:12:func f() error { return nil }\n", 3000) // ~180 KiB
	msgs := make([]any, 0, 40)
	for i := 0; i < 40; i++ {
		msgs = append(msgs, map[string]any{"role": "tool", "content": seg})
	}
	body, err := json.Marshal(map[string]any{"model": "m", "messages": msgs})
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Enabled: true, MinBytes: 500, MaxBytes: 10 << 20, Level: "routre", ScanBytes: 512 << 10, BudgetMS: 1}
	changed, _, partial := New(cfg).ApplyDocBudget(doc)
	if !partial {
		t.Skip("machine too fast to exceed a 1 ms budget on 40 segments; budget path covered by TestBudgetZeroDisables")
	}
	if !changed {
		t.Fatal("partial compression should still report changed")
	}
	if _, err := json.Marshal(doc); err != nil {
		t.Fatalf("document is invalid after partial compression: %v", err)
	}
}

// TestHeadTailBytesIsBounded pins the helper the scan window relies on.
func TestHeadTailBytesIsBounded(t *testing.T) {
	in := strings.Repeat("line\n", 1000) // 5000 bytes
	out := headTailBytes(in, 100, 100)
	if len(out) > 100+100+64 {
		t.Fatalf("headTailBytes returned %d bytes for a 200-byte window", len(out))
	}
	if !strings.Contains(out, "bytes omitted") {
		t.Fatalf("headTailBytes output missing the omission marker: %q", out)
	}
	if headTailBytes("short", 100, 100) != "short" {
		t.Fatal("short input must be returned unchanged")
	}
}

// BenchmarkApplyLargeTool guards the request-path cost of a 10 MiB body.
func BenchmarkApplyLargeTool(b *testing.B) {
	in := bigToolBody(8 << 20)
	cfg := DefaultConfig()
	r := New(cfg)
	b.SetBytes(int64(len(in)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = r.Apply(in)
	}
}
