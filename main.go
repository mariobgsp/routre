// routre-cli: a low-RAM CLI gateway for LLM coding agents.
//
// It exposes an OpenAI-compatible /v1 endpoint on localhost, applies RTK
// token compression and an exact-match cache, and routes requests across
// configured provider tiers with automatic failover and cooldowns.
//
// Usage:
//
//	routre-cli serve  [-config config.json] [-port :20128]
//	routre-cli check  [-config config.json]        # validate config + keys
//	routre-cli start  [--autostart] [-config config.json]   # start daemon (+ enable auto-start)
//	routre-cli stop   [--autostart] [-config config.json]   # stop daemon (+ disable auto-start)
//	routre-cli restart [-config config.json]       # restart the daemon
//	routre-cli bench  [-config config.json] [-target 90]  # token-reduction benchmark
//	routre-cli logs   [-n 50] [-f] [-config config.json]   # tail the request log
//	routre-cli version
package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"routre-cli/internal/cache"
	"routre-cli/internal/config"
	"routre-cli/internal/proxy"
	"routre-cli/internal/reqlog"
	"routre-cli/internal/router"
	"routre-cli/internal/rtk"
	"routre-cli/internal/usage"
)

// version is the release version, injected at build time via
// -ldflags "-X main.version=...". Defaults to "dev" for local builds.
var version = "dev"

func main() {
	logger := log.New(os.Stderr, "[routre-cli] ", log.LstdFlags)
	if err := run(os.Args[1:], logger); err != nil {
		logger.Printf("fatal: %v", err)
		os.Exit(1)
	}
}

func run(args []string, logger *log.Logger) error {
	sub := "serve"
	if len(args) > 0 && args[0][0] != '-' {
		sub = args[0]
		args = args[1:]
	}

	fs := flag.NewFlagSet(sub, flag.ExitOnError)
	cfgPath := fs.String("config", "config.json", "path to config file")
	port := fs.String("port", "", "override listen address (e.g. :20128)")
	target := fs.Float64("target", 90, "bench: required token-reduction %% (0 disables the gate)")
	url := fs.String("url", "http://127.0.0.1:20128", "list: gateway base URL to query")
	asJSON := fs.Bool("json", false, "list: emit JSON instead of the table")
	autostart := fs.Bool("autostart", false, "start: enable auto-start (systemctl enable / launchctl load -w); stop: disable auto-start (systemctl disable / launchctl unload -w)")

	// `logs` owns its own flag set (-n, -f, -config); the shared flags
	// above would reject -n, so skip parsing here and hand the raw args
	// to cmdLogs. Same for `update` (-check).
	if sub != "logs" && sub != "update" {
		if err := fs.Parse(args); err != nil {
			return err
		}
	}

	switch sub {
	case "version":
		fmt.Printf("routre-cli %s\n", version)
		return nil

	case "setup":
		return cmdSetup(*cfgPath, logger)

	case "serve":
		return cmdServe(*cfgPath, *port, logger)

	case "check":
		return cmdCheck(*cfgPath, logger)

	case "start":
		return cmdStart(*cfgPath, *autostart, logger)

	case "stop":
		return cmdStop(*cfgPath, *autostart, logger)

	case "restart":
		return cmdRestart(*cfgPath, logger)

	case "list":
		return cmdList(*cfgPath, *url, *asJSON, logger)

	case "logs":
		return cmdLogs("", args, logger)

	case "update":
		return cmdUpdate(args, logger)

	case "bench":
		return cmdBench(*cfgPath, *target, logger)

	default:
		return fmt.Errorf("unknown subcommand %q (want setup, serve, check, start, stop, restart, list, logs, bench, version)", sub)
	}
}

func cmdServe(cfgPath, port string, logger *log.Logger) error {
	st := config.NewStore(cfgPath)
	if err := st.Load(); err != nil {
		return err
	}
	cfg := st.Get()
	if port != "" {
		st.OverrideListen(port)
		cfg = st.Get()
	}
	logger.Printf("config %s loaded (%d tiers)", cfgPath, len(cfg.Tiers))

	// Usage store persisted under the data dir so `routre-cli list` shows
	// history across restarts. Autosave keeps the ledger fresh on crash/
	// restart (SIGKILL, OOM, power loss) — a shutdown-only save was losing
	// up to hours of token history.
	usagePath := usageFilePath()
	use, err := usage.Load(usagePath)
	if err != nil {
		logger.Printf("usage: %v (starting empty)", err)
		use = usage.New(usagePath)
	}
	const usageAutosaveInterval = 60 * time.Second
	usageTicker := time.NewTicker(usageAutosaveInterval)
	defer usageTicker.Stop()
	go func() {
		for range usageTicker.C {
			if serr := use.Save(); serr != nil {
				logger.Printf("usage autosave failed: %v", serr)
			}
		}
	}()

	rtr := buildRouter(cfg)
	cch := cache.New(cache.Config{
		Enabled: cfg.Cache.Enabled, MaxEntries: cfg.Cache.MaxEntries,
		TTLSeconds: cfg.Cache.TTLSeconds, PrefixOrder: cfg.Cache.PrefixOrder,
	})
	tk := rtk.New(rtk.Config{Enabled: cfg.RTK.Enabled, MinBytes: cfg.RTK.MinBytes, MaxBytes: cfg.RTK.MaxBytes})

	// Model auto-discovery: fetch each provider's own /v1/models list at
	// startup, then refresh periodically and on SIGHUP reload. Additive
	// and best-effort — an unreachable provider is skipped with a
	// warning, never a crash.
	discover := func() {
		rtr.DiscoverModels(nil, func(name string, err error) {
			logger.Printf("model discovery failed for %q: %v", name, err)
		})
	}
	discover()
	discoverStop := make(chan struct{})
	defer close(discoverStop)
	go func() {
		tk := time.NewTicker(router.DiscoveryRefreshInterval)
		defer tk.Stop()
		for {
			select {
			case <-tk.C:
				discover()
			case <-discoverStop:
				return
			}
		}
	}()

	h := proxy.NewHandlers(st, rtr, cch, tk, logger, use)
	// Request log path: the OnLoad callback only fires on reload, so set it
	// explicitly for the initial config too.
	reqlog.SetPath(cfg.RequestLog)
	srv := proxy.New(h, logger)

	// When gateway auth is enabled, seed the shared secret into the keystore
	// and mint a per-process CLI token so `list`/`check`/`logs` keep working.
	if cfg.Auth.SecretEnv != "" {
		if v, ok := os.LookupEnv(cfg.Auth.SecretEnv); ok && v != "" {
			h.Keys.Set(cfg.Auth.SecretEnv, v)
		}
		tok, terr := mintProcessToken()
		if terr != nil {
			return terr
		}
		srv.SetProcessToken(tok)
		logger.Printf("gateway auth enabled; CLI token written to %s", authTokenPath())
	}

	ln, err := srv.Listen(cfg.Listen)
	if err != nil {
		return err
	}
	logger.Printf("listening on %s (rtk=%v cache=%v)", ln.Addr(), tk.Enabled(), cch.Len() == 0)

	// SIGHUP: reload config; SIGINT/SIGTERM: graceful shutdown.
	sig := make(chan os.Signal, 2)
	signal.Notify(sig, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := srv.Serve(ln); err != nil {
			logger.Printf("server error: %v", err)
		}
	}()

	for {
		select {
		case s := <-sig:
			switch s {
			case syscall.SIGHUP:
				if err := st.Reload(); err != nil {
					logger.Printf("reload failed (keeping previous config): %v", err)
				}
				discover() // refresh model lists against the (possibly new) config
				// Persist the ledger on reload too; config reloads often
				// accompany daemon maintenance where a later crash would
				// otherwise cost the unsaved window.
				if serr := use.Save(); serr != nil {
					logger.Printf("usage save on reload failed: %v", serr)
				}
			case syscall.SIGINT, syscall.SIGTERM:
				logger.Printf("shutting down")
				if err := use.Save(); err != nil {
					logger.Printf("usage save failed: %v", err)
				}
				ctxTimeout := 5 * time.Second
				_ = srv.Shutdown(ctxTimeout)
				<-done
				return nil
			}
		case <-done:
			return nil
		}
	}
}

func cmdCheck(cfgPath string, logger *log.Logger) error {
	st := config.NewStore(cfgPath)
	if err := st.Load(); err != nil {
		return err
	}
	cfg := st.Get()
	if len(cfg.Tiers) == 0 {
		logger.Printf("warning: no tiers configured")
	}
	ok := true
	for _, t := range cfg.Tiers {
		for _, p := range t.Providers {
			_, present := os.LookupEnv(p.APIKeyEnv)
			status := "OK"
			if !present {
				status = "MISSING KEY (" + p.APIKeyEnv + ")"
				ok = false
			}
			logger.Printf("tier %-12s provider %-14s kind=%-9s %s", t.Name, p.Name, p.Kind, status)
		}
	}
	if !ok {
		return fmt.Errorf("one or more providers are missing API keys")
	}
	logger.Printf("config OK: %d tiers, %d providers", len(cfg.Tiers), totalProviders(cfg))
	return nil
}

func buildRouter(cfg config.Config) *router.Router {
	tiers := make([]router.TierInput, 0, len(cfg.Tiers))
	for _, t := range cfg.Tiers {
		provs := make([]router.ProviderInput, 0, len(t.Providers))
		for _, p := range t.Providers {
			provs = append(provs, router.ProviderInput{
				Name: p.Name, Kind: string(p.Kind), BaseURL: p.BaseURL,
				APIKeyEnv: p.APIKeyEnv, Models: p.Models, MaxTokens: p.MaxTokens,
			})
		}
		tiers = append(tiers, router.TierInput{Name: t.Name, Providers: provs})
	}
	r := router.New(tiers, router.DefaultCooldownPolicy())
	// Zero-config model handling: forward unknown/future models to all
	// providers (default true). Reset preserves this across reloads.
	r.SetForwardUnknown(cfg.ForwardUnknown)
	return r
}

func totalProviders(c config.Config) int {
	n := 0
	for _, t := range c.Tiers {
		n += len(t.Providers)
	}
	return n
}

// usageFilePath returns the persisted usage location (ROUTRE_CLI_DATA_DIR
// override, else ~/.routre-cli/usage.json).
func usageFilePath() string {
	if dir := os.Getenv("ROUTRE_CLI_DATA_DIR"); dir != "" {
		return filepath.Join(dir, "usage.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".routre-cli", "usage.json")
}

// authTokenPath returns the per-process CLI token location (used when
// gateway auth is enabled, so the local CLI can authenticate without the
// user pasting the shared secret into flags).
func authTokenPath() string {
	if dir := os.Getenv("ROUTRE_CLI_DATA_DIR"); dir != "" {
		return filepath.Join(dir, "auth.tok")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".routre-cli", "auth.tok")
}

// mintProcessToken generates a random 32-byte token, writes it to auth.tok
// (0600), and returns it. Called by serve at startup when auth is enabled.
func mintProcessToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(b)
	path := authTokenPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(tok), 0o600); err != nil {
		return "", err
	}
	return tok, nil
}

// readProcessToken reads the per-process CLI token ("" if absent).
func readProcessToken() string {
	b, err := os.ReadFile(authTokenPath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
