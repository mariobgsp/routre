package reqlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLogAndRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "req.jsonl")
	Log(path, Entry{Client: "opencode", Model: "m", Status: 200, Class: "ok", PromptTokens: 10})
	Log(path, Entry{Client: "codex", Status: 503, Class: "all_failed"})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := 0
	for _, ln := range splitLines(string(data)) {
		var e Entry
		if err := json.Unmarshal([]byte(ln), &e); err != nil {
			t.Fatalf("bad line %q: %v", ln, err)
		}
		lines++
	}
	if lines != 2 {
		t.Fatalf("lines = %d, want 2", lines)
	}
}

func TestLogDisabled(t *testing.T) {
	Log("", Entry{Class: "ignored"}) // must not panic or write
}

func TestSetPathWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "req.jsonl")
	SetPath(path)
	Write(Entry{Class: "ok"})
	SetPath("")
	Write(Entry{Class: "ignored"})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var e Entry
	if err := json.Unmarshal(data, &e); err != nil {
		t.Fatalf("expected one valid JSON line, got %q", data)
	}
	if e.Class != "ok" {
		t.Fatalf("class = %q, want ok (the ignored entry after SetPath(\"\") must not be written)", e.Class)
	}
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
