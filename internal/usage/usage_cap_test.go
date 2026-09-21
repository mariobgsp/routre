package usage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestUsageRowsCapped: the model dimension is client-supplied, so at the cap
// unconfigured models fold into a per-provider "_other" row while every total
// is preserved and configured models keep their own row.
func TestUsageRowsCapped(t *testing.T) {
	s := New("")
	s.SetReservedModels([]string{"real-model"})

	wantPrompt := int64(0)
	for i := 0; i < 600; i++ {
		s.RecordFull("p", fmt.Sprintf("model-%d", i), 2, 1, 0, 0, 0, 0, Prices{}, 0)
		wantPrompt += 2
	}
	s.RecordFull("p", "real-model", 5, 1, 0, 0, 0, 0, Prices{}, 0)
	wantPrompt += 5

	rows := s.Snapshot()
	if len(rows) > maxRows+1 {
		t.Fatalf("rows = %d, want <= %d", len(rows), maxRows+1)
	}
	var gotPrompt, gotRequests int64
	sawOther, sawReal := false, false
	for _, r := range rows {
		gotPrompt += r.PromptTokens
		gotRequests += r.Requests
		switch r.Model {
		case "_other":
			sawOther = true
		case "real-model":
			sawReal = true
		}
	}
	if !sawOther {
		t.Error("expected an _other row once the cap is hit")
	}
	if !sawReal {
		t.Error("a configured model must keep its own row")
	}
	if gotPrompt != wantPrompt {
		t.Errorf("prompt tokens = %d, want %d", gotPrompt, wantPrompt)
	}
	if gotRequests != 601 {
		t.Errorf("requests = %d, want 601", gotRequests)
	}
}

// TestUsageLoadFoldsOverCap: a stale file written before the cap existed must
// not re-open the memory hole at startup, and totals must survive folding.
func TestUsageLoadFoldsOverCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	rows := make([]map[string]any, 0, 600)
	for i := 0; i < 600; i++ {
		rows = append(rows, map[string]any{
			"provider": "p", "model": fmt.Sprintf("m-%d", i),
			"prompt_tokens": 2, "requests": 1,
		})
	}
	data, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	out := s.Snapshot()
	if len(out) > maxRows+1 {
		t.Fatalf("loaded rows = %d, want <= %d", len(out), maxRows+1)
	}
	var prompt int64
	for _, r := range out {
		prompt += r.PromptTokens
	}
	if prompt != 1200 {
		t.Errorf("prompt total after folding = %d, want 1200", prompt)
	}
}
