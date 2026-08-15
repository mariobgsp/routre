package config

import (
	"os"
	"path/filepath"
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
