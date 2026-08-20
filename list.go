package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"routre-cli/internal/config"
	"routre-cli/internal/usage"
)

// cmdList shows everything connected: configured providers (with key and
// live cooldown status from a running gateway) and token/cost usage grouped
// by client (coding agent), with totals. Works fully offline from config +
// persisted usage when the gateway is not running.
func cmdList(cfgPath, url string, logger *log.Logger) error {
	st := config.NewStore(cfgPath)
	if err := st.Load(); err != nil {
		return err
	}
	cfg := st.Get()

	fmt.Println("== configured providers ==")
	if len(cfg.Tiers) == 0 {
		fmt.Println("  (none — run `routre-cli setup`)")
	}
	for _, t := range cfg.Tiers {
		for _, p := range t.Providers {
			_, keySet := lookupKey(p.APIKeyEnv)
			price := "cost n/a"
			if p.PriceIn > 0 || p.PriceOut > 0 {
				price = fmt.Sprintf("$%.4f/M in, $%.4f/M out", p.PriceIn, p.PriceOut)
			}
			keyState := "key MISSING"
			if keySet {
				keyState = "key ok"
			}
			fmt.Printf("  [%s] %-14s %-9s %-8s models=%s %s\n",
				t.Name, p.Name, p.Kind, keyState, strings.Join(p.Models, ","), price)
		}
	}

	// Live status from the gateway (if running).
	status, err := fetchJSON(url + "/v1/status")
	if err == nil {
		fmt.Println("\n== live gateway ==")
		if provs, ok := status["providers"].([]any); ok {
			for _, pr := range provs {
				m, _ := pr.(map[string]any)
				name, _ := m["name"].(string)
				cd, _ := m["cooldown_remaining"].(string)
				fails, _ := m["failures"].(float64)
				state := "up"
				if fails > 0 {
					state = fmt.Sprintf("cooldown %s (fails=%d)", cd, int(fails))
				}
				fmt.Printf("  %-14s %s\n", name, state)
			}
		}
	} else {
		fmt.Println("\n== live gateway ==")
		fmt.Printf("  not reachable at %s (start it with `routre-cli serve`)\n", url)
	}

	// Usage: prefer live /v1/usage, else persisted file.
	rows := []usage.Row{}
	live := false
	u, uerr := fetchJSON(url + "/v1/usage")
	if uerr == nil {
		if ra, ok := u["rows"].([]any); ok {
			for _, r := range ra {
				rows = append(rows, parseRow(r))
			}
		}
		live = true
	}
	if len(rows) == 0 && uerr != nil {
		use, lerr := usage.Load(usageFilePath())
		if lerr == nil {
			rows = use.Snapshot()
		}
	}
	printUsage(rows, live)
	return nil
}

// printUsage renders the token/cost ledger grouped by client, with totals.
// A coding agent is identified by User-Agent; unknown traffic is grouped
// under "unknown". Output goes to stdout.
func printUsage(rows []usage.Row, live bool) {
	printUsageTo(os.Stdout, rows, live)
}

// printUsageTo is printUsage with an explicit output writer (testable).
func printUsageTo(w io.Writer, rows []usage.Row, live bool) {
	fmt.Fprintln(w, "\n== token & cost ledger ==")
	source := "persisted"
	if live {
		source = "live"
	}
	fmt.Fprintf(w, "  source: %s\n", source)

	// Client rows: each client's totals across all providers/models.
	byClient := map[string]*usage.Row{}
	var clients []string
	for _, r := range rows {
		c := r.Provider
		if c == "" {
			c = "unknown"
		}
		if _, ok := byClient[c]; !ok {
			clients = append(clients, c)
			byClient[c] = &usage.Row{Provider: c}
		}
		byClient[c].Add(r)
	}
	if len(clients) == 0 {
		fmt.Fprintln(w, "  no traffic yet — make a request through the gateway")
		return
	}
	sort.Strings(clients)

	// Per-client detail: provider/model rows inside each client's block.
	byClientModel := map[string][]usage.Row{}
	for _, r := range rows {
		k := r.Provider
		byClientModel[k] = append(byClientModel[k], r)
	}

	var totPrompt, totCompletion, totSaved, totRequests int64
	var totCost, totSavedUSD float64

	for _, c := range clients {
		cr := byClient[c]
		totPrompt += cr.PromptTokens
		totCompletion += cr.CompletionTokens
		totSaved += cr.TotalSavedTokens()
		totRequests += cr.Requests
		totCost += cr.CostUSD
		totSavedUSD += cr.SavedUSD

		fmt.Fprintf(w, "\n  %s\n", c)
		fmt.Fprintf(w, "    requests: %d\n", cr.Requests)
		fmt.Fprintf(w, "    consumed: %d tokens (%d in + %d out)\n",
			cr.TotalTokens(), cr.PromptTokens, cr.CompletionTokens)
		fmt.Fprintf(w, "    saved:    %d tokens (rtk %d + cache %d)\n",
			cr.TotalSavedTokens(), cr.RTKSavedTokens, cr.CacheSavedTokens)
		if cr.CacheReadTokens > 0 {
			fmt.Fprintf(w, "    cache read: %d tokens (provider-reported)\n", cr.CacheReadTokens)
		}
		costStr := "n/a (no prices configured)"
		savedStr := "n/a"
		if cr.CostUSD > 0 || cr.SavedUSD > 0 {
			costStr = fmtMoney(cr.CostUSD)
			savedStr = fmtMoney(cr.SavedUSD)
		}
		fmt.Fprintf(w, "    cost:     %s   saved: %s\n", costStr, savedStr)

		rows := byClientModel[c]
		if len(rows) > 1 || rows[0].Model != "" {
			fmt.Fprintf(w, "    by provider/model:\n")
			sort.Slice(rows, func(i, j int) bool {
				if rows[i].Provider != rows[j].Provider {
					return rows[i].Provider < rows[j].Provider
				}
				return rows[i].Model < rows[j].Model
			})
			for _, r := range rows {
				line := fmt.Sprintf("      %s/%s  %d req  %d tok  saved %d",
					r.Provider, r.Model, r.Requests, r.TotalTokens(), r.TotalSavedTokens())
				if r.CacheReadTokens > 0 {
					line += fmt.Sprintf("  cache-read %d", r.CacheReadTokens)
				}
				fmt.Fprintln(w, line)
			}
		}
	}

	fmt.Fprintln(w, "\n  TOTAL")
	fmt.Fprintf(w, "    requests: %d\n", totRequests)
	fmt.Fprintf(w, "    consumed: %d tokens (%d in + %d out)\n", totPrompt+totCompletion, totPrompt, totCompletion)
	fmt.Fprintf(w, "    saved:    %d tokens (%.1f%% of consumed)\n",
		totSaved, pct(totSaved, totPrompt+totCompletion+totSaved))
	totalCostStr := "n/a"
	totalSavedStr := "n/a"
	if totCost > 0 || totSavedUSD > 0 {
		totalCostStr = fmtMoney(totCost)
		totalSavedStr = fmtMoney(totSavedUSD)
	}
	fmt.Fprintf(w, "    cost:     %s   saved: %s\n", totalCostStr, totalSavedStr)
}

// fmtMoney renders USD with enough precision for tiny per-request costs.
func fmtMoney(v float64) string {
	if v >= 0.01 {
		return fmt.Sprintf("$%.4f", v)
	}
	return fmt.Sprintf("$%.6f", v)
}

func pct(saved, base int64) float64 {
	if base <= 0 {
		return 0
	}
	return 100 * float64(saved) / float64(base)
}

// fetchJSON GETs url and decodes a JSON object. Non-200 is an error.
func fetchJSON(url string) (map[string]any, error) {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func parseRow(v any) usage.Row {
	m, ok := v.(map[string]any)
	if !ok {
		return usage.Row{}
	}
	num := func(k string) int64 {
		f, _ := m[k].(float64)
		return int64(f)
	}
	flt := func(k string) float64 {
		f, _ := m[k].(float64)
		return f
	}
	str := func(k string) string {
		s, _ := m[k].(string)
		return s
	}
	return usage.Row{
		Provider:         str("provider"),
		Model:            str("model"),
		PromptTokens:     num("prompt_tokens"),
		CompletionTokens: num("completion_tokens"),
		RTKSavedTokens:   num("rtk_saved_tokens"),
		CacheSavedTokens: num("cache_saved_tokens"),
		CostUSD:          flt("cost_usd"),
		SavedUSD:         flt("saved_usd"),
		Requests:         num("requests"),
	}
}

// lookupKey checks the env var without leaking its value.
func lookupKey(name string) (string, bool) {
	return os.LookupEnv(name)
}
