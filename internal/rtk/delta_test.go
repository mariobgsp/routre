package rtk

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mariobgsp/routre/internal/tokenize"
)

// TestApplyDocSavedDelta pins the per-segment saved-token delta that replaces
// the two whole-body tokenize.Count calls the pipeline used to make. The
// delta must equal the capped-token difference of the rewritten content, not a
// recount of the whole body.
func TestApplyDocSavedDelta(t *testing.T) {
	content := strings.Repeat("internal/x.go:1:func f() error { return nil }\n", 2500) // ~110 KiB, >120 lines
	body, err := json.Marshal(map[string]any{
		"model": "m",
		"messages": []any{
			map[string]any{"role": "tool", "content": content},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var doc map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("decode: %v", err)
	}

	changed, saved := New(DefaultConfig()).ApplyDoc(doc)
	if !changed {
		t.Fatal("RTK did not fire on grep-shaped tool output")
	}
	got := doc["messages"].([]any)[0].(map[string]any)["content"].(string)
	if got == content {
		t.Fatal("content was not rewritten")
	}
	want := tokenize.Estimate(content) - tokenize.Estimate(got)
	if saved != want {
		t.Fatalf("saved delta = %d, want %d (Estimate before - after)", saved, want)
	}
	if saved <= 0 {
		t.Fatalf("saved delta = %d, want > 0", saved)
	}
}

// TestApplyDocDisabled proves ApplyDoc honours the enabled flag without
// touching the document.
func TestApplyDocDisabled(t *testing.T) {
	doc := map[string]any{"messages": []any{map[string]any{"role": "tool", "content": strings.Repeat("x", 1000)}}}
	changed, saved := New(Config{Enabled: false}).ApplyDoc(doc)
	if changed || saved != 0 {
		t.Fatalf("disabled RTK changed=%v saved=%d, want false/0", changed, saved)
	}
}
