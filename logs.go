package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/mariobgsp/routre/internal/config"
	"github.com/mariobgsp/routre/internal/reqlog"
)

// cmdLogs tails the per-request JSONL log for self-debugging:
//
//	routre logs [-n N] [-f] [-config config.json]
//
// The log path is taken from the config's request_log field; when the
// config has no such path (or does not exist), it falls back to
// ~/.routre/requests.jsonl.
func cmdLogs(cfgPath string, args []string, logger *log.Logger) error {
	fs := flag.NewFlagSet("logs", flag.ExitOnError)
	n := fs.Int("n", 50, "number of most recent entries to print (0 = all)")
	follow := fs.Bool("f", false, "follow the log as it grows (tail -f)")
	fs.StringVar(&cfgPath, "config", cfgPath, "path to config file (request_log path is read from it)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	path := logPathFromConfig(cfgPath)
	if path == "" {
		path = defaultReqLogPath()
	}
	if path == "" {
		return fmt.Errorf("no request log configured (set request_log in %s)", cfgPath)
	}

	if *follow {
		return followLog(path, logger)
	}
	return printTail(path, *n)
}

// logPathFromConfig returns the request_log path from the config file, or
// "" when unset/unreadable.
func logPathFromConfig(cfgPath string) string {
	st := config.NewStore(cfgPath)
	if err := st.Load(); err != nil {
		return ""
	}
	return st.Get().RequestLog
}

// defaultReqLogPath mirrors the gateway's default data dir.
func defaultReqLogPath() string {
	if dir := os.Getenv("ROUTRE_CLI_DATA_DIR"); dir != "" {
		return filepath.Join(dir, "requests.jsonl")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".routre", "requests.jsonl")
}

// printTail prints the last n lines of the log (n<=0: all lines).
func printTail(path string, n int) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %v", path, err)
	}
	lines := splitLinesKeepEmpty(data)
	start := 0
	if n > 0 && len(lines) > n {
		start = len(lines) - n
	}
	for _, l := range lines[start:] {
		if l == "" {
			continue
		}
		fmt.Println(formatLogLine(l))
	}
	return nil
}

// followLog tails the file: prints existing entries (last 50) then polls
// for new ones every 500ms.
func followLog(path string, logger *log.Logger) error {
	if err := printTail(path, 50); err != nil {
		logger.Printf("warning: %v (waiting for the log to appear)", err)
	}
	logger.Printf("following %s (ctrl-C to stop)", path)
	offset := fileSize(path)
	for {
		time.Sleep(500 * time.Millisecond)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if len(data) > offset {
			for _, l := range splitLinesKeepEmpty(data[offset:]) {
				if l == "" {
					continue
				}
				fmt.Println(formatLogLine(l))
			}
			offset = len(data)
		}
	}
}

// formatLogLine renders one JSONL entry as a compact human-readable line;
// unparseable lines are echoed verbatim.
func formatLogLine(line string) string {
	var e reqlog.Entry
	if err := json.Unmarshal([]byte(line), &e); err != nil {
		return line
	}
	ts := e.Time
	if len(ts) > 19 {
		ts = ts[:19] // RFC3339 without zone: keep it short
	}
	var b []byte
	b = append(b, ts...)
	if e.Client != "" {
		b = append(b, " client="...)
		b = append(b, e.Client...)
	}
	if e.Model != "" {
		b = append(b, " model="...)
		b = append(b, e.Model...)
	}
	up := e.UpstreamModel
	if up == "" {
		up = e.Model
	}
	if up != e.Model {
		b = append(b, " → "...)
		b = append(b, up...)
	}
	if e.Provider != "" {
		b = append(b, " @"...)
		b = append(b, e.Provider...)
	}
	if e.Class != "" {
		b = append(b, " "...)
		b = append(b, e.Class...)
	}
	if e.Status != 0 {
		b = append(b, ' ')
		b = append(b, fmt.Sprintf("%d", e.Status)...)
	}
	if e.LatencyMS > 0 {
		b = append(b, " "...)
		b = append(b, fmt.Sprintf("%dms", e.LatencyMS)...)
	}
	if e.PromptTokens > 0 || e.CompletionTokens > 0 {
		b = append(b, " "...)
		b = append(b, fmt.Sprintf("in=%d out=%d", e.PromptTokens, e.CompletionTokens)...)
	}
	if e.RTKSavedTokens > 0 {
		b = append(b, " "...)
		b = append(b, fmt.Sprintf("rtk=%d", e.RTKSavedTokens)...)
	}
	if e.CacheReadTokens > 0 {
		b = append(b, " "...)
		b = append(b, fmt.Sprintf("cache-read=%d", e.CacheReadTokens)...)
	}
	return string(b)
}

func splitLinesKeepEmpty(data []byte) []string {
	var out []string
	start := 0
	for i := 0; i < len(data); i++ {
		if data[i] == '\n' {
			out = append(out, string(data[start:i]))
			start = i + 1
		}
	}
	if start < len(data) {
		out = append(out, string(data[start:]))
	}
	return out
}

func fileSize(path string) int {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return int(fi.Size())
}
