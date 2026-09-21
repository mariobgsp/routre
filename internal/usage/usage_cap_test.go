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

// TestUsageLoadDefersFoldToReservedModels: Load must NOT fold on its own (it
// does not know the configured models yet — the gateway calls SetReservedModels
// right after), because folding without the reserved set can bury a configured
// model under "_other" for the whole session. Once reservations are known the
// over-cap rows fold AROUND the configured model, preserving every total.
func TestUsageLoadDefersFoldToReservedModels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	rows := make([]map[string]any, 0, 600)
	for i := 0; i < 599; i++ {
		rows = append(rows, map[string]any{
			"provider": "p", "model": fmt.Sprintf("m-%d", i),
			"prompt_tokens": 2, "requests": 1,
		})
	}
	rows = append(rows, map[string]any{
		"provider": "p", "model": "real-model", "prompt_tokens": 5, "requests": 1,
	})
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
	if n := len(s.Snapshot()); n != 600 {
		t.Fatalf("Load kept %d rows, want all 600: it must defer folding until the reserved set is known", n)
	}

	s.SetReservedModels([]string{"real-model"})
	out := s.Snapshot()
	if len(out) > maxRows+1 {
		t.Fatalf("rows after folding = %d, want <= %d", len(out), maxRows+1)
	}
	var prompt int64
	sawReal := false
	for _, r := range out {
		prompt += r.PromptTokens
		if r.Model == "real-model" {
			sawReal = true
			if r.Provider != "p" {
				t.Errorf("real-model row provider = %q, want p", r.Provider)
			}
		}
	}
	if !sawReal {
		t.Fatal("a configured model was folded into _other at Load")
	}
	if want := int64(599*2 + 5); prompt != want {
		t.Errorf("prompt total after folding = %d, want %d", prompt, want)
	}
}
