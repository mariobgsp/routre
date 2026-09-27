package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDefaultsWhenMissing(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "nope.json"))
	if err := s.Load(); err != nil {
		t.Fatalf("missing file must not error: %v", err)
	}
	if s.Get().Listen != "127.0.0.1:20128" {
		t.Fatalf("unexpected default listen: %s", s.Get().Listen)
	}
}

func TestLoadAndValidate(t *testing.T) {
	p := writeTemp(t, `{
		"listen": "127.0.0.1:20128",
		"tiers": [
			{"name": "subscription", "providers": [
				{"name": "anthropic-sub", "kind": "anthropic", "base_url": "https://api.anthropic.com", "api_key_env": "ANTHROPIC_API_KEY", "models": ["claude-sonnet-4-5"]}
			]},
			{"name": "cheap", "providers": [
				{"name": "glm", "kind": "openai", "base_url": "https://api.z.ai", "api_key_env": "GLM_API_KEY", "models": ["glm-4.7"]}
			]}
		]
	}`)
	s := NewStore(p)
	if err := s.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	cfg := s.Get()
	if len(cfg.Tiers) != 2 || cfg.Tiers[0].Providers[0].Name != "anthropic-sub" {
		t.Fatalf("unexpected config: %+v", cfg.Tiers)
	}
}

func TestInvalidKindRejected(t *testing.T) {
	p := writeTemp(t, `{"listen":":0","tiers":[{"name":"t","providers":[
		{"name":"x","kind":"huggingface","base_url":"http://x","api_key_env":"K","models":["m"]}
	]}]}`)
	s := NewStore(p)
	if err := s.Load(); err == nil {
		t.Fatal("invalid kind must fail validation")
	}
}

func TestDuplicateProviderRejected(t *testing.T) {
	p := writeTemp(t, `{"listen":":0","tiers":[
		{"name":"a","providers":[{"name":"dup","kind":"openai","base_url":"http://a","api_key_env":"K","models":["m"]}]},
		{"name":"b","providers":[{"name":"dup","kind":"openai","base_url":"http://b","api_key_env":"K","models":["m"]}]}
	]}`)
	s := NewStore(p)
	if err := s.Load(); err == nil {
		t.Fatal("duplicate provider names must fail")
	}
}

func TestReloadAppliesCallback(t *testing.T) {
	p := writeTemp(t, `{"listen":"127.0.0.1:1","tiers":[]}`)
	s := NewStore(p)
	got := ""
	s.SetOnLoad(func(c Config) { got = c.Listen })
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	if got != "127.0.0.1:1" {
		t.Fatalf("callback not called on load: %q", got)
	}
	writeTemp2 := func(content string) {
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeTemp2(`{"listen":"127.0.0.1:2","tiers":[]}`)
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	if got != "127.0.0.1:2" {
		t.Fatalf("callback not called on reload: %q", got)
	}
}

func TestInvalidReloadKeepsPrevious(t *testing.T) {
	p := writeTemp(t, `{"listen":"127.0.0.1:1","tiers":[]}`)
	s := NewStore(p)
	_ = s.Load()
	writeTemp2 := func(content string) {
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeTemp2(`not json`)
	if err := s.Reload(); err == nil {
		t.Fatal("invalid reload must error")
	}
	if s.Get().Listen != "127.0.0.1:1" {
		t.Fatal("previous config must be retained")
	}
}

func TestLoadReloadShareBehavior(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"listen":"127.0.0.1:1","tiers":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewStore(path)
	if err := s.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := s.Get().Listen; got != "127.0.0.1:1" {
		t.Fatalf("Load listen = %q", got)
	}
	if err := os.WriteFile(path, []byte(`{"listen":"127.0.0.1:2","tiers":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got := s.Get().Listen; got != "127.0.0.1:2" {
		t.Fatalf("Reload listen = %q", got)
	}
}

func TestMergeModelIDs(t *testing.T) {
	cases := []struct {
		name       string
		existing   []string
		discovered []string
		wantMerged []string
		wantAdded  []string
	}{
		{"appends new ids sorted", []string{"m"}, []string{"m2", "m1"}, []string{"m", "m1", "m2"}, []string{"m1", "m2"}},
		{"never prunes", []string{"m1", "m2"}, []string{"m1"}, []string{"m1", "m2"}, nil},
		{"keeps existing order", []string{"z", "a"}, []string{"a", "b"}, []string{"z", "a", "b"}, []string{"b"}},
		{"empty discovered is a no-op", []string{"m"}, nil, []string{"m"}, nil},
		{"duplicate discovered counted once", []string{"m"}, []string{"x", "x"}, []string{"m", "x"}, []string{"x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			merged, added := MergeModelIDs(tc.existing, tc.discovered)
			if !slices.Equal(merged, tc.wantMerged) {
				t.Fatalf("merged = %v, want %v", merged, tc.wantMerged)
			}
			if !slices.Equal(added, tc.wantAdded) {
				t.Fatalf("added = %v, want %v", added, tc.wantAdded)
			}
		})
	}
}
