// Package reqlog implements the optional per-request JSONL log: one line
// per chat request with routing outcome, token counts, and latency. The
// log path comes from the config `request_log` field ("" = disabled).
// Lines are appended under a mutex; the file is opened lazily and re-opened
// on config reload when the path changes.
package reqlog

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// Entry is one JSONL line. All fields are optional; zero values are omitted.
type Entry struct {
	Time             string  `json:"ts"`
	Client           string  `json:"client,omitempty"`
	Model            string  `json:"model,omitempty"`
	Provider         string  `json:"provider,omitempty"`
	UpstreamModel    string  `json:"upstream_model,omitempty"`
	Stream           bool    `json:"stream,omitempty"`
	Status           int     `json:"status,omitempty"`
	Class            string  `json:"class,omitempty"` // request outcome class: ok/cache/failover/error
	PromptTokens     int64   `json:"prompt_tokens,omitempty"`
	CompletionTokens int64   `json:"completion_tokens,omitempty"`
	RTKSavedTokens   int64   `json:"rtk_saved_tokens,omitempty"`
	CacheReadTokens  int64   `json:"cache_read_tokens,omitempty"`
	CostUSD          float64 `json:"cost_usd,omitempty"`
	LatencyMS        int64   `json:"latency_ms,omitempty"`
}

// Log appends one line to path. When path is empty the line is written
// to stderr so a misconfigured request_log does not silently swallow
// observability — the operator still sees the request.
func Log(path string, e Entry) {
	if path == "" {
		if e.Time == "" {
			e.Time = time.Now().Format(time.RFC3339)
		}
		if data, err := json.Marshal(e); err == nil {
			fmt.Fprintln(os.Stderr, string(data))
		}
		return
	}
	if e.Time == "" {
		e.Time = time.Now().Format(time.RFC3339)
	}
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(data, '\n'))
}

// Mutex-guarded writer used by the gateway for sequential appends (the
// gateway serializes chat requests through Handlers anyway, but the guard
// keeps the contract explicit for tests that fire concurrent requests).
type mutexLog struct {
	mu   sync.Mutex
	path string
}

var global = &mutexLog{}

// SetPath switches the global log destination ("" disables).
func SetPath(path string) {
	global.mu.Lock()
	global.path = path
	global.mu.Unlock()
}

// Write appends e to the current log path.
func Write(e Entry) {
	global.mu.Lock()
	p := global.path
	global.mu.Unlock()
	Log(p, e)
}
