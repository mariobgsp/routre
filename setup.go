package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"routre-cli/internal/config"
)

// cmdSetup runs the interactive wizard: it collects the listen address,
// provider tiers (name, kind, base URL, models), optional API keys, and
// optional prices, then writes:
//
//	config.json       — provider/URL/tier configuration (no secrets)
//	routre-cli.env    — API keys, loaded automatically by serve/check/list
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

	fmt.Println("routre-cli setup — interactive configuration")
	fmt.Println("(API keys are written to routre-cli.env, never into the config)")
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
		models := ask("models (comma-separated)", "")
		if models == "" {
			logger.Printf("skipped: at least one model required")
			continue
		}
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

	// Write routre-cli.env next to the config (0600 on POSIX).
	envPath := config.EnvFilePath(cfgPath)
	if err := flushEnvFile(envPath); err != nil {
		return err
	}

	fmt.Println()
	fmt.Printf("wrote %s and %s\n", cfgPath, envPath)
	fmt.Println("next: `routre-cli serve -config " + cfgPath + "` then point your coding agent at http://127.0.0.1:20128")
	fmt.Println("      `routre-cli check -config " + cfgPath + "` validates keys, `routre-cli list` shows usage")
	return nil
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
