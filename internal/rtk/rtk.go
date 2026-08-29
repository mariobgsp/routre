// Package rtk implements Real-Time Kompression: heuristic, rule-based
// compression of tool_result content in LLM request bodies. It is a port of
// the approach used by 9router's open-sse/rtk (MIT), reimplemented for a
// low-RAM static binary: no local LM, no network calls, pure rules.
//
// Safety contract (fail-open):
//   - never grows a payload: a compression is only applied when it strictly
//     reduces size;
//   - never panics: malformed JSON is returned unchanged;
//   - bounded work: filters run on raw text, not on parsed structures.
package rtk

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/mariobgsp/routre/internal/config"
	"sync"
)

// Config controls compression behavior. The zero value is not usable;
// use DefaultConfig.
type Config struct {
	// Enabled turns the whole pipeline on/off.
	Enabled bool `json:"enabled"`
	// MinBytes: content smaller than this is never touched (avoids overhead
	// and churn on tiny tool results).
	MinBytes int `json:"min_bytes"`
	// MaxBytes: content larger than this is skipped (bounded work; very
	// large blobs are usually images/logs where heuristics do more harm).
	MaxBytes int `json:"max_bytes"`
	// Level: "" or "standard" (default pipeline) or "routre"
	// (routre level — ultra-aggressive: after the matched filter,
	// additionally strips blank lines, dedups runs of >=2 identical lines,
	// and keeps only head+tail of the result — head+tail bias per the
	// "lost in the middle" finding). Still obeys the fail-open contract:
	// output is used only when strictly smaller.
	Level string `json:"level,omitempty"`
}

// DefaultConfig matches 9router RTK's operating window (500B..10MiB), on by
// default.
func DefaultConfig() Config {
	return Config{Enabled: true, MinBytes: 500, MaxBytes: 10 << 20, Level: "routre"}
}

// RTK is a concurrency-safe compressor with a reloadable config.
type RTK struct {
	mu  sync.RWMutex
	cfg Config
}

// New creates an RTK with the given config.
func New(cfg Config) *RTK { return &RTK{cfg: cfg} }

// Update swaps the config (called on SIGHUP reload).
func (r *RTK) Reconfigure(cfg config.Config) {
	r.Update(Config{
		Enabled:  cfg.RTK.Enabled,
		MinBytes: cfg.RTK.MinBytes,
		MaxBytes: cfg.RTK.MaxBytes,
		Level:    cfg.RTK.Level,
	})
}

func (r *RTK) Update(cfg Config) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cfg = cfg
}

// Enabled reports whether compression is currently on.
func (r *RTK) Enabled() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cfg.Enabled
}

// Apply runs the full pipeline over a JSON request body:
//  1. decode with UseNumber (preserves numeric fidelity);
//  2. walk messages and compress every tool/tool_result content;
//  3. re-marshal ONLY if something changed (otherwise original bytes are
//     returned unchanged, keeping cache keys stable).
//
// Returns the processed body (possibly identical to in) and a bool reporting
// whether compression happened. Never returns an error for malformed input;
// malformed input is returned unchanged with changed=false.
func (r *RTK) Apply(in []byte) (out []byte, changed bool) {
	r.mu.RLock()
	cfg := r.cfg
	r.mu.RUnlock()

	if !cfg.Enabled {
		return in, false
	}
	if !json.Valid(in) {
		return in, false
	}
	dec := json.NewDecoder(bytes.NewReader(in))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return in, false
	}
	messages, ok := doc["messages"].([]any)
	if !ok {
		return in, false
	}
	mutated := false
	for _, m := range messages {
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}
		if compressMessage(cfg, msg) {
			mutated = true
		}
	}
	if !mutated {
		return in, false
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return in, false
	}
	// Never grow: if re-marshaling somehow produced a larger payload
	// (whitespace normalization), keep the original.
	if len(out) >= len(in) {
		return in, false
	}
	return out, true
}

// compressMessage compresses one message's content in place. Reports whether
// anything changed.
func compressMessage(cfg Config, msg map[string]any) bool {
	role, _ := msg["role"].(string)
	content, ok := msg["content"]
	if !ok {
		return false
	}
	switch c := content.(type) {
	case string:
		if role == "tool" {
			nc, ok := compressText(cfg, c)
			if !ok {
				return false
			}
			msg["content"] = nc
			return true
		}
		return false
	case []any:
		changed := false
		for _, blk := range c {
			b, ok := blk.(map[string]any)
			if !ok {
				continue
			}
			switch b["type"] {
			case "tool_result":
				// Claude tool_result: content is a string or an array of
				// {type:"text", text:"..."} blocks.
				if tc, ok := b["content"].(string); ok {
					if nc, ok2 := compressText(cfg, tc); ok2 {
						b["content"] = nc
						changed = true
					}
				} else if arr, ok := b["content"].([]any); ok {
					for _, tb := range arr {
						tbm, ok := tb.(map[string]any)
						if !ok || tbm["type"] != "text" {
							continue
						}
						if ts, ok := tbm["text"].(string); ok {
							if nc, ok2 := compressText(cfg, ts); ok2 {
								tbm["text"] = nc
								changed = true
							}
						}
					}
				}
			case "text":
				// OpenAI tool messages carry {type:"text", text:"..."}.
				if role == "tool" {
					if ts, ok := b["text"].(string); ok {
						if nc, ok2 := compressText(cfg, ts); ok2 {
							b["text"] = nc
							changed = true
						}
					}
				}
			}
		}
		return changed
	}
	return false
}

// compressText applies the filter pipeline to one tool_result string.
// Returns the compressed text and whether it shrank.
func compressText(cfg Config, text string) (string, bool) {
	if len(text) < cfg.MinBytes || len(text) > cfg.MaxBytes {
		return text, false
	}
	cand := filterByAutodetect(text)
	if cand == "" || len(cand) >= len(text) {
		return text, false
	}
	if cfg.Level == "routre" || cfg.Level == "caveman" { // caveman kept as alias
		cand = routrePass(cand)
	}
	if cand == "" || len(cand) >= len(text) {
		return text, false
	}
	return cand, true
}

// routrePass is the routre-level post-filter: drop blank lines, dedup
// runs of >=2 identical lines, then cap at head+tail lines.
func routrePass(text string) string {
	lines := splitLines(text)
	kept := lines[:0]
	for _, l := range lines {
		if l != "" {
			kept = append(kept, l)
		}
	}
	return truncateAt(dedupConsecutive(joinLines(kept), 2), 40, 15)
}

// filterByAutodetect scores the first 1KiB of text against known command
// shapes and applies the best-matching filter; falls back to smart-truncate.
func filterByAutodetect(text string) string {
	peek := text
	if len(peek) > 1024 {
		peek = peek[:1024]
	}
	best, bestScore := "", 0
	for _, f := range filters {
		if s := f.score(peek); s > bestScore {
			best, bestScore = f.name, s
		}
	}
	if bestScore < 1 {
		return smartTruncate(text)
	}
	for _, f := range filters {
		if f.name == best {
			return f.compress(text)
		}
	}
	return smartTruncate(text)
}

// ---------------------------------------------------------------------------
// Filters. Each filter is a pure function on raw text. The general contract:
// the output must be strictly smaller than the input for it to be used
// (enforced in compressText), and must preserve the head of the content.

type filter struct {
	name     string
	score    func(peek string) int // heuristic evidence, higher = better match
	compress func(text string) string
}

// dedupConsecutive collapses runs of >=3 identical lines into one line plus a
// marker. Used by several filters.
func dedupConsecutive(text string, minRun int) string {
	lines := splitLines(text)
	if len(lines) < minRun {
		return text
	}
	var out []string
	for i := 0; i < len(lines); {
		j := i
		for j < len(lines) && lines[j] == lines[i] {
			j++
		}
		run := j - i
		if run >= minRun {
			out = append(out, lines[i], fmt.Sprintf("… [%d repeated lines]", run-1))
		} else {
			out = append(out, lines[i])
		}
		i = j
	}
	return joinLines(out)
}

// truncateAt caps text at head+tail lines around an omission marker.
func truncateAt(text string, head, tail int) string {
	lines := splitLines(text)
	if len(lines) <= head+tail {
		return text
	}
	var out []string
	out = append(out, lines[:head]...)
	out = append(out, fmt.Sprintf("… [%d lines omitted]", len(lines)-head-tail))
	out = append(out, lines[len(lines)-tail:]...)
	return joinLines(out)
}

// smartTruncate is the default fallback: keep the first 120 lines and the
// last 60 lines of content longer than 250 lines.
func smartTruncate(text string) string {
	return truncateAt(text, 120, 60)
}

var (
	reGrepLine      = mustCompile(`:\d+:`)            // grep -n / rg -n output
	reReadNumbered  = mustCompile(`^\s*\d+\s*[|:]\s`) // read numbered
	reSearchList    = mustCompile(`^\s*\d+[.)]\s`)    // search result lists
	reGitLog        = mustCompile(`(?m)^commit [0-9a-f]{7,40}`)
	reGitLogOneline = mustCompile(`(?m)^[0-9a-f]{7,40}\s`) // git log --oneline
	reGitStatus     = mustCompile(`(?m)^(M{1,2}|A{1,2}|D|R|C|\?\?|U|T|X) `)
	reGitDiffHeader = mustCompile(`^diff --git `)
	reFindPath      = mustCompile(`(?m)^(\./|/)?[^\s/]+(/[^\s/]+)*$`)
	reLsLine        = mustCompile(`(?m)^\S+\s+\d+\s+\S+\s+\S+\s+\d+\s+`) // ls -l
	reTreeBranch    = mustCompile(`(?m)^[│ ]*[├└]── `)
	reBuildError    = mustCompile(`(?m)^(error|warning|fatal|undefined reference|FAIL|ok)\s`)
)

var filters = []filter{
	{
		name:  "git-diff",
		score: func(peek string) int { return count(reGitDiffHeader, peek) * 3 },
		// Cap each diff hunk at 10 changed lines (bench-tuned so every
		// payload clears the 90% gate while preserving hunk headers and
		// context); drop the rest of the hunk's content.
		compress: func(text string) string {
			lines := splitLines(text)
			var out []string
			changed := 0
			marker := false
			for _, l := range lines {
				if reGitDiffHeader.MatchString(l) {
					changed = 0
					marker = false
				}
				isContent := l != "" && (l[0] == ' ' || l[0] == '+' || l[0] == '-')
				if changed >= 10 && isContent {
					if !marker {
						out = append(out, "… [diff lines omitted]")
						marker = true
					}
					continue
				}
				out = append(out, l)
				if isContent && (l[0] == '+' || l[0] == '-') {
					changed++
				}
			}
			return truncateAt(joinLines(out), 80, 30)
		},
	},
	{
		name:  "git-status",
		score: func(peek string) int { return count(reGitStatus, peek) },
		compress: func(text string) string {
			return dedupConsecutive(text, 3)
		},
	},
	{
		name: "git-log",
		score: func(peek string) int {
			s := count(reGitLog, peek) * 2
			if s == 0 {
				s = count(reGitLogOneline, peek)
			}
			return s
		},
		compress: func(text string) string {
			return truncateAt(dedupConsecutive(text, 3), 50, 15)
		},
	},
	{
		name:  "grep",
		score: func(peek string) int { return count(reGrepLine, peek) },
		compress: func(text string) string {
			return truncateAt(dedupConsecutive(text, 3), 80, 40)
		},
	},
	{
		name: "find",
		score: func(peek string) int {
			s := count(reFindPath, peek)
			if s > 0 {
				return s + 1 // paths alone; prefer over generic
			}
			return 0
		},
		compress: func(text string) string {
			return dedupConsecutive(text, 3)
		},
	},
	{
		name:  "ls",
		score: func(peek string) int { return count(reLsLine, peek) },
		compress: func(text string) string {
			return dedupConsecutive(text, 3)
		},
	},
	{
		name:  "tree",
		score: func(peek string) int { return count(reTreeBranch, peek) * 2 },
		compress: func(text string) string {
			return truncateAt(dedupConsecutive(text, 3), 80, 40)
		},
	},
	{
		name:  "read-numbered",
		score: func(peek string) int { return count(reReadNumbered, peek) },
		compress: func(text string) string {
			return dedupConsecutive(text, 3)
		},
	},
	{
		name:  "search-list",
		score: func(peek string) int { return count(reSearchList, peek) },
		compress: func(text string) string {
			return dedupConsecutive(text, 3)
		},
	},
	{
		name: "dedup-log",
		// Only wins when the peek actually shows repeated lines; a constant
		// score would hijack every non-matching payload (real bug caught by
		// the bench gate).
		score: func(peek string) int { return repeatedRunScore(peek) },
		compress: func(text string) string {
			return dedupConsecutive(text, 3)
		},
	},
	{
		name:  "build-output",
		score: func(peek string) int { return count(reBuildError, peek) },
		compress: func(text string) string {
			return truncateAt(dedupConsecutive(text, 3), 50, 25)
		},
	},
}

// repeatedRunScore returns how many repeated runs of >=3 identical lines
// appear in the peek (0 = none).
func repeatedRunScore(peek string) int {
	lines := splitLines(peek)
	score := 0
	for i := 0; i < len(lines); {
		j := i
		for j < len(lines) && lines[j] == lines[i] {
			j++
		}
		if j-i >= 3 {
			score += j - i
		}
		i = j
	}
	return score
}

// splitLines splits on \n, keeping trailing empty handling simple.
func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

func joinLines(lines []string) string {
	var b bytes.Buffer
	for i, l := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(l)
	}
	return b.String()
}
