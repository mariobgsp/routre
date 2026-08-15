package rtk

import (
	"encoding/json"
	"strings"
	"testing"
)

func testRTK() *RTK {
	return New(DefaultConfig())
}

func TestApplyCompressesOpenAIToolMessage(t *testing.T) {
	r := testRTK()
	big := strings.Repeat("same line\n", 400) // > 250 lines, dedup-able
	body := `{"model":"gpt-5","messages":[` +
		`{"role":"user","content":"hi"},` +
		`{"role":"tool","content":` + jsonStr(big) + `}]}`

	out, changed := r.Apply([]byte(body))
	if !changed {
		t.Fatal("expected compression to happen")
	}
	if len(out) >= len(body) {
		t.Fatalf("payload must never grow: %d -> %d", len(body), len(out))
	}
	// Must remain valid JSON with the same message structure.
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	msgs := doc["messages"].([]any)
	tool := msgs[1].(map[string]any)
	if tool["role"] != "tool" {
		t.Fatalf("role changed: %v", tool["role"])
	}
	content := tool["content"].(string)
	if !strings.Contains(content, "[") || !strings.Contains(content, "repeated") {
		t.Fatalf("expected dedup marker in output, got: %.100s", content)
	}
}

func TestApplyCompressesClaudeToolResult(t *testing.T) {
	r := testRTK()
	big := strings.Repeat("zzzz zzzz zzzz zzzz\n", 300)
	body := `{"model":"claude-sonnet-4-5","max_tokens":1000,"messages":[` +
		`{"role":"user","content":"go"},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"bash","input":{"cmd":"ls"}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":` +
		jsonStr(big) + `}]}]}`

	out, changed := r.Apply([]byte(body))
	if !changed {
		t.Fatal("expected compression")
	}
	if len(out) >= len(body) {
		t.Fatalf("payload grew: %d -> %d", len(body), len(out))
	}
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	msgs := doc["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)
	blocks := last["content"].([]any)
	tr := blocks[0].(map[string]any)
	if tr["type"] != "tool_result" {
		t.Fatalf("tool_result block lost: %v", tr["type"])
	}
}

func TestApplyCompressesClaudeToolResultTextBlocks(t *testing.T) {
	r := testRTK()
	big := strings.Repeat("block line\n", 300)
	body := `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[` +
		`{"type":"text","text":` + jsonStr(big) + `}]}]}]}`

	out, changed := r.Apply([]byte(body))
	if !changed {
		t.Fatal("expected compression of text blocks")
	}
	if len(out) >= len(body) {
		t.Fatalf("payload grew: %d -> %d", len(body), len(out))
	}
}

func TestSmallContentUntouched(t *testing.T) {
	r := testRTK()
	body := `{"messages":[{"role":"tool","content":"tiny"}]}`
	out, changed := r.Apply([]byte(body))
	if changed || !strings.EqualFold(string(out), body) {
		t.Fatalf("small content must be untouched: changed=%v", changed)
	}
}

func TestMalformedInputFailOpen(t *testing.T) {
	r := testRTK()
	out, changed := r.Apply([]byte(`{"messages":[`))
	if changed || string(out) != `{"messages":[` {
		t.Fatal("malformed input must pass through unchanged")
	}
}

func TestDisabledDoesNothing(t *testing.T) {
	r := New(Config{Enabled: false})
	big := strings.Repeat("x\n", 500)
	body := `{"messages":[{"role":"tool","content":` + jsonStr(big) + `}]}`
	out, changed := r.Apply([]byte(body))
	if changed || string(out) != body {
		t.Fatal("disabled RTK must not touch the body")
	}
}

func TestNeverGrowsOnUncompressibleContent(t *testing.T) {
	r := testRTK()
	// Random-ish single-line content: no filter should shrink it, and even
	// if one does, Apply must not grow the payload.
	random := strings.Repeat("a1b2c3d4e5f6g7h8i9j0", 1000)
	body := `{"messages":[{"role":"tool","content":` + jsonStr(random) + `}]}`
	out, changed := r.Apply([]byte(body))
	if changed && len(out) >= len(body) {
		t.Fatalf("payload grew: %d -> %d", len(body), len(out))
	}
	if !changed {
		// Acceptable: nothing shrank, nothing changed.
	}
}

func TestUpdateReload(t *testing.T) {
	r := testRTK()
	r.Update(Config{Enabled: false})
	if r.Enabled() {
		t.Fatal("Update did not apply")
	}
}

func TestGitDiffFilter(t *testing.T) {
	var b strings.Builder
	b.WriteString("diff --git a/x.go b/x.go\n")
	for i := 0; i < 1000; i++ {
		b.WriteString("+line of change content here\n")
	}
	got := filterByAutodetect(b.String())
	if len(got) >= len(b.String()) {
		t.Fatalf("git-diff filter must shrink: %d -> %d", b.Len(), len(got))
	}
	if !strings.HasPrefix(got, "diff --git") {
		t.Fatalf("head must be preserved: %.40s", got)
	}
}

func TestSmartTruncate(t *testing.T) {
	short := strings.Repeat("short line\n", 10)
	if got := smartTruncate(short); got != short {
		t.Fatal("short content must pass through untouched")
	}
	long := strings.Repeat("content line\n", 500)
	got := smartTruncate(long)
	if len(got) >= len(long) {
		t.Fatal("long content must shrink")
	}
	if !strings.Contains(got, "lines omitted") {
		t.Fatal("truncation marker missing")
	}
}

func TestDedupConsecutive(t *testing.T) {
	in := strings.Repeat("dup\n", 50) + "unique\n"
	got := dedupConsecutive(in, 3)
	if strings.Count(got, "dup") != 1 {
		t.Fatalf("expected single kept line, got:\n%s", got)
	}
	if !strings.Contains(got, "repeated") {
		t.Fatal("marker missing")
	}
}

// jsonStr produces a quoted JSON string literal.
func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
