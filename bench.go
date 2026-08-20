package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"routre-cli/internal/config"
	"routre-cli/internal/rtk"
	"routre-cli/internal/tokenize"
)

// cmdBench measures RTK token reduction over realistic tool-heavy request
// bodies (benchdata/*.json) and gates the result against a target reduction
// percentage (default 90%).
//
// Metric definition (honest version):
//   - "tool tokens" = estimated tokens inside tool/tool_result content ONLY;
//   - "payload tokens" = estimated tokens of the whole request body;
//   - tool_reduction% = 100 * (1 - tool_tokens_after / tool_tokens_before);
//   - payload_reduction% = 100 * (1 - payload_after / payload_before).
//
// The 90% gate applies to tool_reduction (the RTK guarantee). payload
// reduction is reported separately because it depends on how much of a real
// session is tool output.
func cmdBench(cfgPath string, targetPct float64, logger *log.Logger) error {
	st := config.NewStore(cfgPath)
	if err := st.Load(); err != nil {
		return err
	}
	cfg := st.Get()
	tk := rtk.New(rtk.Config{Enabled: cfg.RTK.Enabled, MinBytes: cfg.RTK.MinBytes, MaxBytes: cfg.RTK.MaxBytes})

	pattern := filepath.Join("benchdata", "*.json")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		// Fall back to the directory next to the binary, so `routre-cli
		// bench` works when installed globally (npm -g etc.).
		if exe, exeErr := os.Executable(); exeErr == nil {
			alt := filepath.Join(filepath.Dir(exe), "benchdata", "*.json")
			if f2, gErr := filepath.Glob(alt); gErr == nil && len(f2) > 0 {
				files = f2
			}
		}
	}
	if len(files) == 0 {
		return fmt.Errorf("no benchdata found at %s (or next to the binary); run from the repo root", pattern)
	}

	type result struct {
		file            string
		payloadBefore   int
		payloadAfter    int
		toolBefore      int
		toolAfter       int
		compressedCount int
	}

	var totalPayloadBefore, totalPayloadAfter, totalToolBefore, totalToolAfter int
	var results []result

	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		pb := tokenize.Count(string(data), tokenize.KindOpenAI)
		tb := toolTokens(data)
		out, changed := tk.Apply(data)
		pa := tokenize.Count(string(out), tokenize.KindOpenAI)
		ta := toolTokens(out)
		res := result{
			file:          filepath.Base(f),
			payloadBefore: pb, payloadAfter: pa,
			toolBefore: tb, toolAfter: ta,
		}
		if changed {
			res.compressedCount++
		}
		results = append(results, res)
		totalPayloadBefore += pb
		totalPayloadAfter += pa
		totalToolBefore += tb
		totalToolAfter += ta
	}

	fmt.Println("routre-cli bench — RTK token reduction")
	fmt.Println("metric: BPE token count (cl100k_base, embedded); see internal/tokenize")
	fmt.Println()
	fmt.Printf("%-28s %10s %10s %10s %10s %6s\n", "file", "payload→", "payload%", "tool→", "tool%", "touched")
	payloadRed, toolRed := 0.0, 0.0
	for _, r := range results {
		pr, tr := 0.0, 0.0
		if r.payloadBefore > 0 {
			pr = 100 * (1 - float64(r.payloadAfter)/float64(r.payloadBefore))
		}
		if r.toolBefore > 0 {
			tr = 100 * (1 - float64(r.toolAfter)/float64(r.toolBefore))
		}
		payloadRed += pr
		toolRed += tr
		touched := "-"
		if r.compressedCount > 0 {
			touched = "yes"
		}
		fmt.Printf("%-28s %10d %9.1f%% %10d %9.1f%% %6s\n",
			r.file, r.payloadAfter, pr, r.toolAfter, tr, touched)
	}
	n := len(results)
	payloadRed /= float64(n)
	toolRed /= float64(n)

	// Per-payload gate: every file must clear the target, not just the
	// aggregate (a lazy filter that ignores one payload shape would hide
	// behind the others).
	worst := 100.0
	worstFile := ""
	for _, r := range results {
		if r.toolBefore == 0 {
			continue
		}
		tr := 100 * (1 - float64(r.toolAfter)/float64(r.toolBefore))
		if tr < worst {
			worst = tr
			worstFile = r.file
		}
	}
	fmt.Println()
	fmt.Printf("aggregate payload reduction : %5.1f%%  (%d → %d est. tokens)\n", payloadRed, totalPayloadBefore, totalPayloadAfter)
	fmt.Printf("aggregate TOOL reduction   : %5.1f%%  (%d → %d est. tokens)  [RTK guarantee]\n", toolRed, totalToolBefore, totalToolAfter)
	fmt.Printf("per-payload worst           : %5.1f%% (%s)\n", worst, worstFile)
	fmt.Printf("target gate                : %5.1f%% on tool reduction (aggregate AND per-payload)\n", targetPct)

	if targetPct <= 0 {
		fmt.Println("result: gate disabled (-target 0); reporting only")
		return nil
	}
	if toolRed+0.01 < targetPct {
		return fmt.Errorf("BENCH FAIL: aggregate tool reduction %.1f%% < target %.1f%%", toolRed, targetPct)
	}
	if worst+0.01 < targetPct {
		return fmt.Errorf("BENCH FAIL: per-payload tool reduction %.1f%% (%s) < target %.1f%%", worst, worstFile, targetPct)
	}
	fmt.Println("result: PASS")
	return nil
}

// toolTokens estimates the tokens inside tool/tool_result content of a
// request body. It mirrors the shapes rtk.Apply touches.
func toolTokens(body []byte) int {
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		return 0
	}
	msgs, _ := doc["messages"].([]any)
	total := 0
	for _, m := range msgs {
		msg, _ := m.(map[string]any)
		role, _ := msg["role"].(string)
		content, _ := msg["content"].(any)
		switch c := content.(type) {
		case string:
			if role == "tool" {
				total += tokenize.Count(c, tokenize.KindOpenAI)
			}
		case []any:
			for _, blk := range c {
				b, _ := blk.(map[string]any)
				switch b["type"] {
				case "tool_result":
					if s, ok := b["content"].(string); ok {
						total += tokenize.Count(s, tokenize.KindOpenAI)
					} else if arr, ok := b["content"].([]any); ok {
						for _, tb := range arr {
							if tbm, ok := tb.(map[string]any); ok {
								if s, ok := tbm["text"].(string); ok {
									total += tokenize.Count(s, tokenize.KindOpenAI)
								}
							}
						}
					}
				case "text":
					if role == "tool" {
						if s, ok := b["text"].(string); ok {
							total += tokenize.Count(s, tokenize.KindOpenAI)
						}
					}
				}
			}
		}
	}
	return total
}
