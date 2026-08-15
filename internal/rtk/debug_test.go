package rtk

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestDebugGitDiff inspects the filter output on the real bench payload.
func TestDebugGitDiff(t *testing.T) {
	data, err := os.ReadFile("../../benchdata/git-diff.json")
	if err != nil {
		t.Skip("benchdata not available")
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	msgs := doc["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)
	blocks := last["content"].([]any)
	tr := blocks[0].(map[string]any)
	content := tr["content"].(string)

	out := filterByAutodetect(content)
	t.Logf("input:  %d bytes, %d lines", len(content), len(splitLines(content)))
	t.Logf("output: %d bytes, %d lines", len(out), len(splitLines(out)))
	kept := 0
	for _, l := range splitLines(out) {
		if strings.HasPrefix(l, "+") || strings.HasPrefix(l, "-") {
			kept++
		}
	}
	t.Logf("kept change lines: %d", kept)
}
