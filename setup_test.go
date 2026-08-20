package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"routre-cli/internal/config"
)

// TestMarshalConfigValidJSON ensures the config the wizard writes parses and
// validates (the wizard must never emit an unloadable config).
func TestMarshalConfigValidJSON(t *testing.T) {
	cfg := config.Default()
	cfg.Listen = "127.0.0.1:20128"
	cfg.Tiers = []config.Tier{{
		Name: "subscription",
		Providers: []config.Provider{{
			Name: "openrouter", Kind: config.KindOpenAI,
			BaseURL: "https://openrouter.ai/api/v1", APIKeyEnv: "OPENROUTER_API_KEY",
			Models: []string{"tencent/hy3"},
		}},
	}}
	data, err := marshalConfig(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got config.Config
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("config does not parse: %v", err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("config does not validate: %v", err)
	}
	if got.Listen != "127.0.0.1:20128" || got.Tiers[0].Providers[0].Name != "openrouter" {
		t.Fatalf("round-trip mismatch: %s", data)
	}
}

// TestFlushEnvFile0600 verifies the wizard's key file is written with 0600
// perms and the KEY=VALUE format the loader expects.
func TestFlushEnvFile0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routre-cli.env")
	envLines = envLines[:0]
	appendEnvLine("A_KEY", "secret-value")
	appendEnvLine("B_KEY", "other")
	if err := flushEnvFile(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, "A_KEY=secret-value") || !strings.Contains(s, "B_KEY=other") {
		t.Fatalf("env file content wrong: %q", s)
	}
	if fi, err := os.Stat(path); err == nil {
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("env file perms = %o, want 600", fi.Mode().Perm())
		}
	}
	envLines = envLines[:0]
}
