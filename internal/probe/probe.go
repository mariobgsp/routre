// Package probe runs lightweight periodic health checks against each
// configured provider so cooldown / availability changes are visible
// between real requests. Strictly observation-only: probes never
// touch the router cooldown, the cache, the usage ledger, or the
// metrics counters — they are a side channel that can only log.
//
// ponytail: a probe that escalates cooldowns would amplify the very
// outage it is meant to surface (probe flapping → cooldown cascade →
// no real traffic gets through). The contract here is "do no harm".
package probe

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mariobgsp/routre/internal/config"
	"github.com/mariobgsp/routre/internal/keystore"
	"github.com/mariobgsp/routre/internal/proxy/failures"
	"github.com/mariobgsp/routre/internal/reqlog"
)

// probeTimeout bounds a single probe request. Shorter than the main
// process's 20s dial/header timeout so a slow provider cannot delay
// the next probe.
const probeTimeout = 8 * time.Second

// minimumInterval is the floor on the configured interval. Smaller
// values are clamped to keep probes from stacking on a slow provider.
const minimumInterval = 5 * time.Second

// Probe is a single observation result. The struct is the public type
// so tests can assert on it without parsing log lines.
type Result struct {
	Provider string
	Kind     string
	Model    string
	Status   int // HTTP status (0 on network error)
	Latency  time.Duration
	Err      error
	Class    string // "ok", "server", "auth", "rateLimit", "network", "client"
}

// Config wires a probe. Provider and KeyStore are required; the rest
// has safe defaults.
type Config struct {
	Logger     *log.Logger
	HTTPClient *http.Client
	// Keys is read-only after construction; probes never mutate it.
	// A *keystore.Store satisfies this; the probe looks up via Get().
	Keys *keystore.Store
	// ProbeModel overrides the model used for probes. When empty, the
	// first listed model on each provider is used.
	ProbeModel string
	// Now is the clock; injected for tests.
	Now func() time.Time
}

// Probe is stateless: one HTTP call, one Result. No ticker, no
// global. Tests call Probe directly with an httptest.Server.
type Probe struct {
	cfg Config
}

// NewProbe builds a stateless probe (one-shot). Use for `routre doctor`
// or direct tests.
func NewProbe(c Config) *Probe {
	if c.Logger == nil {
		c.Logger = log.Default()
	}
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: probeTimeout}
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return &Probe{cfg: c}
}

// Do runs one probe against p. model overrides the probe's default.
func (pr *Probe) Do(provider config.Provider, model string) Result {
	return pr.probeOne(provider, model)
}

// Runner is the periodic loop. Prefer Probe for one-shot; Runner for
// the daemon. Kept for compat (ProbeOne delegates to Probe).
type Runner struct {
	probe   *Probe
	store   *config.Store // live config; nil = use Probe's ProbeModel only
	stop    chan struct{}
	stopped chan struct{}
	once    sync.Once
}

// New builds a Runner. Call Start to actually begin probing. Prefer
// NewProbe for one-shot use.
func New(c Config) *Runner {
	return &Runner{probe: NewProbe(c), stop: make(chan struct{}), stopped: make(chan struct{})}
}

// NewWithStore builds a Runner that refreshes config from store on each
// tick (SIGHUP-aware). If store is nil the global is used (compat).
func NewWithStore(c Config, store *config.Store) *Runner {
	r := New(c)
	r.store = store
	return r
}

// Start launches the probe loop. Returns immediately; the loop runs
// until Stop. interval is clamped to minimumInterval when shorter.
func (r *Runner) Start(interval time.Duration) {
	if interval < minimumInterval {
		interval = minimumInterval
	}
	go r.loop(interval)
}

// Stop signals the probe loop to exit and waits for it to finish.
// Idempotent; safe to call multiple times.
func (r *Runner) Stop() {
	r.once.Do(func() { close(r.stop) })
	<-r.stopped
}

func (r *Runner) loop(interval time.Duration) {
	defer close(r.stopped)
	t := time.NewTicker(interval)
	defer t.Stop()
	r.tick()
	for {
		select {
		case <-r.stop:
			return
		case <-t.C:
			r.tick()
		}
	}
}

func (r *Runner) tick() {
	cfg := r.currentConfig()
	if cfg == nil {
		return
	}
	for _, tier := range cfg.Tiers {
		for _, p := range tier.Providers {
			if p.APIKeyEnv == "" {
				continue
			}
			if r.probe.cfg.Keys != nil {
				if _, ok := r.probe.cfg.Keys.Get(p.APIKeyEnv); !ok {
					continue
				}
			}
			res := r.probe.probeOne(p, r.probe.cfg.ProbeModel)
			r.logResult(res)
		}
	}
}

func (r *Runner) currentConfig() *config.Config {
	if r.store != nil {
		c := r.store.Get()
		return &c
	}
	return currentConfig()
}

// ProbeOne runs a single probe against p. The Runner's ProbeModel is
// used unless model is non-empty. Exposed for `routre doctor`.
func (r *Runner) ProbeOne(p config.Provider, model string) Result {
	return r.probe.Do(p, model)
}

// probeOne sends a single minimal chat request to the provider and
// returns the outcome. Never escalates cooldown.
func (pr *Probe) probeOne(p config.Provider, model string) Result {
	res := Result{Provider: p.Name, Kind: string(p.Kind)}
	if model == "" && len(p.Models) > 0 {
		model = p.Models[0]
	}
	res.Model = model
	if model == "" {
		res.Class = "client"
		res.Err = fmt.Errorf("no model to probe (provider has empty models list)")
		return res
	}
	key, ok := pr.cfg.Keys.Get(p.APIKeyEnv)
	if !ok || key == "" {
		res.Class = "auth"
		res.Err = fmt.Errorf("provider key %s not set", p.APIKeyEnv)
		return res
	}
	// Build a minimal OpenAI-shape request. Cross-kind providers
	// (anthropic/gemini) get translated upstream by the main pipeline;
	// here we just send the OpenAI shape to a provider that speaks
	// OpenAI. For non-openai providers we send the OpenAI shape to
	// their OpenAI-compatible endpoint and rely on the gateway's
	// model routing. This is good enough for a liveness signal.
	payload := map[string]any{
		"model":      model,
		"messages":   []any{map[string]any{"role": "user", "content": "ping"}},
		"max_tokens": 1,
		"stream":     false,
	}
	body, _ := json.Marshal(payload)
	base := strings.TrimRight(p.BaseURL, "/")
	path := "/v1/chat/completions"
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, base+path, bytes.NewReader(body))
	if err != nil {
		res.Class = "client"
		res.Err = err
		return res
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	if p.Kind == "anthropic" {
		req.Header.Set("X-Api-Key", key)
		req.Header.Set("Anthropic-Version", "2023-06-01")
	}
	if strings.Contains(p.BaseURL, "opencode.ai") {
		// Opencode session header required from 09/06.
		req.Header.Set("x-opencode-session", probeOpencodeSession())
	}
	start := pr.cfg.Now()
	resp, err := pr.cfg.HTTPClient.Do(req)
	res.Latency = pr.cfg.Now().Sub(start)
	if err != nil {
		res.Class = "network"
		res.Err = err
		return res
	}
	defer resp.Body.Close()
	// Drain and discard; we only need the status.
	_, _ = bytes.NewBuffer(nil).ReadFrom(resp.Body)
	res.Status = resp.StatusCode
	res.Class = classifyStatus(resp.StatusCode)
	if resp.StatusCode >= 400 {
		res.Err = fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return res
}

func classifyStatus(status int) string {
	switch {
	case status >= 200 && status < 300:
		return "ok"
	case status == 401 || status == 403:
		return "auth"
	case status == 429:
		return "rateLimit"
	case status >= 500:
		return "server"
	default:
		return "client"
	}
}

// logResult writes one reqlog line. Uses the synthetic client
// "probe" so it can be filtered with `routre logs -provider <name>`.
// Class "healthcheck" so `-errors` skips it (it is itself the
// health observation, not a real user failure). The human log line
// reuses failures.RenderHuman so probe + doctor + 503 all share the
// same per-provider shape.
func (r *Runner) logResult(res Result) {
	cls := "healthcheck"
	if res.Class != "ok" {
		cls = "healthcheck_" + res.Class
	}
	entry := reqlog.Entry{
		Client:    "probe",
		Model:     res.Provider + "/" + res.Model,
		Provider:  res.Provider,
		Status:    res.Status,
		Class:     cls,
		LatencyMS: res.Latency.Milliseconds(),
	}
	if res.Err != nil {
		// Reuse PromptTokens field for the error string to keep the
		// JSONL schema simple; the request log header does not have
		// a dedicated error column.
		entry.PromptTokens = 0
	}
	reqlog.Write(entry)
	l := r.probe.cfg.Logger
	if l == nil {
		l = log.Default()
	}
	outcome := failures.Outcome{Provider: res.Provider, Kind: res.Kind, Class: res.Class}
	if res.Err != nil {
		outcome.Err = res.Err.Error()
	}
	l.Printf("probe %s/%s (%dms):", res.Provider, res.Model, res.Latency.Milliseconds())
	failures.RenderHuman(logWriter{l}, []failures.Outcome{outcome})
}

// logWriter adapts *log.Logger to io.Writer. log.Logger has Printf but
// no Write; failures.RenderHuman needs io.Writer so probe and doctor
// can share the per-provider format.
type logWriter struct{ l *log.Logger }

func (w logWriter) Write(p []byte) (int, error) {
	w.l.Print(string(p))
	return len(p), nil
}

// currentConfig is set by the main process via SetConfig. Probes are
// decoupled from the gateway's *config.Store so they cannot block on
// the config mutex.
var (
	cfgMu sync.RWMutex
	cfg   *config.Config
)

// SetConfig publishes the active config for the probe loop to read.
// Called by main on startup and on SIGHUP reload. A nil value means
// probes have no current config to read.
func SetConfig(c *config.Config) {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	if c == nil {
		cfg = nil
		return
	}
	cfg = c
}

func currentConfig() *config.Config {
	cfgMu.RLock()
	defer cfgMu.RUnlock()
	return cfg
}

var (
	probeSessID   string
	probeSessOnce sync.Once
)

func probeOpencodeSession() string {
	probeSessOnce.Do(func() {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err == nil {
			probeSessID = hex.EncodeToString(b)
		} else {
			probeSessID = "routre-fallback-session"
		}
	})
	return probeSessID
}
