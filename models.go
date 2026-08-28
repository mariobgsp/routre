package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"runtime"
	"sort"
	"strings"
	"syscall"

	"github.com/mariobgsp/routre/internal/config"
)

// cmdModels dispatches `routre models <verb>`. Verbs:
//
//	sync — fetch each provider's /v1/models and persist new IDs to config.json
//	diff — show what sync would add, without writing
func cmdModels(args []string, logger *log.Logger) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprintln(os.Stderr, "usage: routre models sync [-dry-run] [-prune] [-json] [-config config.json]")
		fmt.Fprintln(os.Stderr, "       routre models diff  [-prune] [-json] [-config config.json]  (dry-run alias)")
		return nil
	}
	verb := args[0]
	rest := args[1:]
	switch verb {
	case "sync", "diff":
		return cmdModelsSync(rest, logger, verb == "diff")
	default:
		return fmt.Errorf("unknown models verb %q (want sync, diff)", verb)
	}
}

func cmdModelsSync(args []string, logger *log.Logger, dryRunForced bool) error {
	fs := flag.NewFlagSet("models sync", flag.ContinueOnError)
	cfgPath := fs.String("config", "config.json", "path to config file")
	dryRun := fs.Bool("dry-run", false, "show what would change without writing")
	prune := fs.Bool("prune", false, "remove models no longer advertised (default: additive only)")
	asJSON := fs.Bool("json", false, "emit machine-readable diff as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if dryRunForced {
		*dryRun = true
	}

	st := config.NewStore(*cfgPath)
	if err := st.Load(); err != nil {
		return err
	}
	cfg := st.Get()
	if len(cfg.Tiers) == 0 {
		return fmt.Errorf("no tiers configured (run `routre setup`)")
	}
	// Ensure env keys loaded (config.Store.Load already loaded routre.env)
	// Build router and discover via the shared discovery path.
	rtr := buildRouter(cfg)
	var warns []string
	rtr.DiscoverModels(nil, func(name string, err error) {
		warns = append(warns, fmt.Sprintf("%s: %v", name, err))
		logger.Printf("model discovery failed for %q: %v", name, err)
	})

	// Snapshot discovered models per provider.
	type provStatus struct {
		name   string
		models []string
		ok     bool // discovery succeeded (provider was reachable)
	}
	statusByName := map[string]provStatus{}
	warnedSet := map[string]bool{}
	for _, w := range warns {
		// warn format is "name: err", extract name
		if i := strings.Index(w, ":"); i >= 0 {
			warnedSet[strings.TrimSpace(w[:i])] = true
		}
	}
	for _, s := range rtr.Status() {
		ok := !warnedSet[s.Provider]
		statusByName[s.Provider] = provStatus{name: s.Provider, models: append([]string(nil), s.Models...), ok: ok}
	}

	// Build new config with merged models and compute diff.
	type diffEntry struct {
		Provider string   `json:"provider"`
		Added    []string `json:"added"`
		Removed  []string `json:"removed,omitempty"`
		Before   int      `json:"before"`
		After    int      `json:"after"`
		Skipped  bool     `json:"skipped,omitempty"`
		Reason   string   `json:"reason,omitempty"`
	}
	var diffs []diffEntry
	newCfg := cfg
	totalAdded, totalRemoved := 0, 0
	changed := false

	for ti := range newCfg.Tiers {
		for pi := range newCfg.Tiers[ti].Providers {
			p := &newCfg.Tiers[ti].Providers[pi]
			st, ok := statusByName[p.Name]
			if !ok {
				diffs = append(diffs, diffEntry{Provider: p.Name, Before: len(p.Models), After: len(p.Models), Skipped: true, Reason: "not in router"})
				continue
			}
			if !st.ok {
				diffs = append(diffs, diffEntry{Provider: p.Name, Before: len(p.Models), After: len(p.Models), Skipped: true, Reason: "discovery failed — keeping config"})
				continue
			}
			// Discovery succeeded; st.models is the merged in-memory list.
			// For additive mode, keep original plus any new discovered IDs.
			// For prune mode, use discovered list verbatim (sorted, deduped).
			beforeSet := map[string]bool{}
			for _, m := range p.Models {
				beforeSet[m] = true
			}
			var after []string
			if *prune {
				after = append([]string(nil), st.models...)
				sort.Strings(after)
			} else {
				// Additive: original order preserved, new IDs appended sorted.
				after = append([]string(nil), p.Models...)
				seen := map[string]bool{}
				for _, m := range after {
					seen[m] = true
				}
				var added []string
				for _, m := range st.models {
					if !seen[m] {
						added = append(added, m)
					}
				}
				sort.Strings(added)
				after = append(after, added...)
			}
			// Compute added/removed for reporting.
			afterSet := map[string]bool{}
			for _, m := range after {
				afterSet[m] = true
			}
			var added, removed []string
			for _, m := range after {
				if !beforeSet[m] {
					added = append(added, m)
				}
			}
			for _, m := range p.Models {
				if !afterSet[m] {
					removed = append(removed, m)
				}
			}
			if len(added) > 0 || len(removed) > 0 {
				changed = true
			}
			totalAdded += len(added)
			totalRemoved += len(removed)
			sort.Strings(added)
			sort.Strings(removed)
			diffs = append(diffs, diffEntry{
				Provider: p.Name,
				Added:    added,
				Removed:  removed,
				Before:   len(p.Models),
				After:    len(after),
			})
			p.Models = after
		}
	}

	if *asJSON {
		out := map[string]any{
			"dry_run":       *dryRun,
			"prune":         *prune,
			"total_added":   totalAdded,
			"total_removed": totalRemoved,
			"providers":     diffs,
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
		return nil
	}

	// Human output.
	for _, d := range diffs {
		if d.Skipped {
			fmt.Printf("  %-18s skipped (%s) — %d models\n", d.Provider, d.Reason, d.Before)
			continue
		}
		if len(d.Added) == 0 && len(d.Removed) == 0 {
			fmt.Printf("  %-18s up to date — %d models\n", d.Provider, d.Before)
			continue
		}
		fmt.Printf("  %-18s %d → %d", d.Provider, d.Before, d.After)
		if len(d.Added) > 0 {
			fmt.Printf("  +%d %s", len(d.Added), strings.Join(d.Added, ", "))
		}
		if len(d.Removed) > 0 {
			fmt.Printf("  -%d %s", len(d.Removed), strings.Join(d.Removed, ", "))
		}
		fmt.Println()
	}
	if !changed {
		fmt.Println("models: no changes")
		if len(warns) > 0 {
			fmt.Printf("warnings: %d provider(s) unreachable (kept config)\n", len(warns))
		}
		return nil
	}
	if *dryRun {
		fmt.Printf("dry-run: %d added, %d removed (use without -dry-run to write %s)\n", totalAdded, totalRemoved, *cfgPath)
		return nil
	}
	if err := st.Save(newCfg); err != nil {
		return fmt.Errorf("save %s: %w", *cfgPath, err)
	}
	fmt.Printf("wrote %s — %d added, %d removed\n", *cfgPath, totalAdded, totalRemoved)
	// Best-effort SIGHUP live gateway so it picks up new models without restart.
	if err := notifyGateway(); err != nil {
		// Not fatal; config is saved, gateway will discover on next startup/SIGHUP/ticker.
		logger.Printf("gateway reload: %v (restart `routre serve` to apply)", err)
	} else {
		fmt.Println("gateway reloaded (SIGHUP)")
	}
	return nil
}

// notifyGateway sends SIGHUP to a running routre daemon (if any) so the
// persisted model list takes effect without a manual restart.
func notifyGateway() error {
	if runtime.GOOS == "windows" {
		return fmt.Errorf("SIGHUP not supported on windows — restart `routre serve` to apply")
	}
	// Port-scan mirrors stop.go: find the listener and HUP it.
	port := listenPort("config.json")
	// Try config-derived port first, then default port.
	pid, err := pidOnPort(runtime.GOOS, port)
	if err != nil || pid == 0 {
		if port != defaultPort {
			pid, _ = pidOnPort(runtime.GOOS, defaultPort)
		}
	}
	if pid == 0 {
		return fmt.Errorf("no running gateway found on :%d", port)
	}
	return killHUP(pid)
}

func killHUP(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(syscall.SIGHUP)
}
