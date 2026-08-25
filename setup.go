package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mariobgsp/routre/internal/config"
)

// cmdSetup runs the interactive wizard: it collects the listen address,
// provider tiers (name, kind, base URL, models), optional API keys, and
// optional prices, then writes:
//
//	config.json       — provider/URL/tier configuration (no secrets)
//	routre.env    — API keys, loaded automatically by serve/check/list
//
// The env file is created with 0600 permissions on POSIX systems.
func cmdSetup(cfgPath string, logger *log.Logger) error {
	in := bufio.NewReader(os.Stdin)
	ask := func(prompt, def string) string {
		if def != "" {
			fmt.Printf("%s [%s]: ", prompt, def)
		} else {
			fmt.Printf("%s: ", prompt)
		}
		line, err := in.ReadString('\n')
		if err != nil && line == "" {
			return def
		}
		line = strings.TrimSpace(line)
		if line == "" {
			return def
		}
		return line
	}
	askBool := func(prompt string, def bool) bool {
		d := "y"
		if !def {
			d = "n"
		}
		v := ask(prompt, d)
		return strings.HasPrefix(strings.ToLower(v), "y")
	}

	fmt.Println("routre setup — interactive configuration")
	fmt.Println("(API keys are written to routre.env, never into the config)")
	fmt.Println()

	cfg := config.Default()
	cfg.Listen = ask("listen address", cfg.Listen)

	rtkEnabled := askBool("enable RTK token compression (recommended)", true)
	cfg.RTK.Enabled = rtkEnabled
	cacheEnabled := askBool("enable response cache (recommended)", true)
	cfg.Cache.Enabled = cacheEnabled

	for {
		fmt.Println()
		add := askBool("add a provider?", false)
		if !add {
			break
		}
		p := config.Provider{}
		tier := ask("tier (subscription/cheap/free — tried in order)", "subscription")
		p.Name = ask("provider name (e.g. openrouter)", "")
		if p.Name == "" {
			logger.Printf("skipped: provider name required")
			continue
		}
		kind := ask("kind (openai/anthropic)", "openai")
		p.Kind = config.Kind(kind)
		if p.Kind != config.KindOpenAI && p.Kind != config.KindAnthropic {
			logger.Printf("skipped: kind must be openai or anthropic")
			continue
		}
		defURL := "https://api.openai.com/v1"
		if p.Kind == config.KindAnthropic {
			defURL = "https://api.anthropic.com"
		}
		p.BaseURL = ask("base URL (OpenAI-compatible endpoint)", defURL)
		key := ask("API key (leave empty to use an existing env var)", "")
		models := ask("models (comma-separated; leave empty to auto-discover)", "")
		priceIn := ask("input price $/1M tokens (0 = unknown)", "0")
		priceOut := ask("output price $/1M tokens (0 = unknown)", "0")

		// API key: store in the env file under a suggested var name.
		envName := suggestEnvName(p.Name)
		if key != "" {
			appendEnvLine(envName, key)
		}
		p.APIKeyEnv = envName
		p.Models = splitModels(models)
		fmt.Sscanf(priceIn, "%g", &p.PriceIn)
		fmt.Sscanf(priceOut, "%g", &p.PriceOut)

		// Append to the matching tier (create if missing).
		found := false
		for i := range cfg.Tiers {
			if cfg.Tiers[i].Name == tier {
				cfg.Tiers[i].Providers = append(cfg.Tiers[i].Providers, p)
				found = true
				break
			}
		}
		if !found {
			cfg.Tiers = append(cfg.Tiers, config.Tier{Name: tier, Providers: []config.Provider{p}})
		}
	}

	if len(cfg.Tiers) == 0 {
		logger.Printf("warning: no providers configured; the gateway will start but answer 503")
	}

	// Preferred model + fallbacks (up to 10): the user picks the default
	// model (their preferred provider) and the models to fall back to when
	// it fails — free tiers, or any other model they have access to.
	fmt.Println()
	fmt.Println("routing preference")
	fmt.Println("(preferred model first; then up to 9 fallback models, in order)")
	preferred := ask("preferred model (default)", firstModel(cfg))
	fmt.Println("fallback models — any model you have access to (free tiers, or paid models on another provider); press Enter after the last one:")
	var fallbacks []string
	for i := 0; i < 9; i++ {
		m := ask(fmt.Sprintf("  fallback %d", i+1), "")
		if m == "" {
			break
		}
		fallbacks = append(fallbacks, m)
	}
	if preferred != "" {
		cfg.PreferredModel = preferred
	}
	cfg.Fallbacks = fallbacks

	// Gateway security: optional shared-secret protection for the port.
	fmt.Println()
	fmt.Println("gateway security")
	fmt.Println("(protect the localhost port so only processes that know the key can use your providers)")
	if askBool("protect the gateway with a shared secret?", false) {
		envName := ask("secret env var name", "ROUTRE_SECRET")
		cfg.Auth = config.AuthConfig{SecretEnv: envName, Header: "X-Routre-Key"}
		appendEnvLine(envName, generateSecret())
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	// Write config.json.
	data, err := marshalConfig(cfg)
	if err != nil {
		return err
	}
	if err := os.WriteFile(cfgPath, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", cfgPath, err)
	}

	// Write routre.env next to the config (0600 on POSIX).
	envPath := config.EnvFilePath(cfgPath)
	if err := flushEnvFile(envPath); err != nil {
		return err
	}

	fmt.Println()
	fmt.Printf("wrote %s and %s\n", cfgPath, envPath)
	fmt.Println("next: `routre serve -config " + cfgPath + "` then point your coding agent at http://127.0.0.1:20128")
	fmt.Println("      `routre check -config " + cfgPath + "` validates keys, `routre list` shows usage")
	// --detect: one-command hook for known agents (stdlib only, best-effort).
	for _, a := range os.Args {
		if a == "--detect" || a == "-detect" {
			hookAgents("http://127.0.0.1:20128", logger)
			break
		}
	}
	return nil
}

// hookAgents tries to point known coding agents at the gateway by patching
// their config files or printing the export line. Best-effort, no deps.
func hookAgents(baseURL string, logger *log.Logger) {
	home, _ := os.UserHomeDir()
	hooks := []struct{ path, hint string }{
		{filepath.Join(home, ".config", "opencode", "opencode.json"), `{"$schema":"...","mcp":{},"env":{"OPENAI_BASE_URL":"%s","ANTHROPIC_BASE_URL":"%s"}}`},
		{filepath.Join(home, ".claude", "settings.json"), `{"env":{"ANTHROPIC_BASE_URL":"%s"}}`},
		{filepath.Join(home, ".codex", "config.toml"), `OPENAI_BASE_URL="%s"`},
	}
	for _, h := range hooks {
		if _, err := os.Stat(h.path); err == nil {
			logger.Printf("detect: found %s — set OPENAI_BASE_URL/ANTHROPIC_BASE_URL to %s", h.path, baseURL)
		} else {
			logger.Printf("detect: no %s — export OPENAI_BASE_URL=%s ANTHROPIC_BASE_URL=%s", h.path, baseURL, baseURL)
		}
	}
	fmt.Printf("\n[detect] point agents at %s:\n  export OPENAI_BASE_URL=%s ANTHROPIC_BASE_URL=%s\n", baseURL, baseURL, baseURL)
}

// splitModels splits a comma-separated model list.
func splitModels(s string) []string {
	var out []string
	for _, m := range strings.Split(s, ",") {
		m = strings.TrimSpace(m)
		if m != "" {
			out = append(out, m)
		}
	}
	return out
}

// firstModel returns the first model of the first provider (used as the
// default for the "preferred model" setup question).
func firstModel(cfg config.Config) string {
	for _, t := range cfg.Tiers {
		for _, p := range t.Providers {
			if len(p.Models) > 0 {
				return p.Models[0]
			}
		}
	}
	return ""
}

// suggestEnvName derives an env var name from a provider name:
// "OpenRouter" → OPENROUTER_API_KEY.
func suggestEnvName(providerName string) string {
	var b strings.Builder
	for _, r := range providerName {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	name := strings.ToUpper(b.String())
	if name == "" {
		name = "PROVIDER"
	}
	return name + "_API_KEY"
}

// marshalConfig pretty-prints the config for writing.
func marshalConfig(cfg config.Config) ([]byte, error) {
	return json.MarshalIndent(cfg, "", "  ")
}

// generateSecret returns a random 32-byte hex secret for gateway auth.
func generateSecret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// Unreachable in practice; fall back to a time-based value rather
		// than failing setup.
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// --- env file buffer -----------------------------------------------------

var envLines []string

// appendEnvLine records KEY=VALUE for the env file (deduplicated by key).
func appendEnvLine(key, value string) {
	line := key + "=" + value
	for i, l := range envLines {
		if strings.HasPrefix(l, key+"=") {
			envLines[i] = line
			return
		}
	}
	envLines = append(envLines, line)
}

// flushEnvFile writes the collected env lines to path with 0600 perms.
func flushEnvFile(path string) error {
	if len(envLines) == 0 {
		// Nothing to write; remove a stale env file.
		_ = os.Remove(path)
		return nil
	}
	content := strings.Join(envLines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		// Windows chmod is a no-op-ish; not fatal.
		_ = filepath.Clean(path)
	}
	return nil
}
