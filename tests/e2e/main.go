// Command e2e is the routre end-to-end smoke test: it builds the real
// routre CLI and the mock upstream, starts both against a throwaway
// HOME/config, and drives one routed chat request through the gateway.
//
// Everything it touches lives under a temp dir — the real ~/.routre, ~/.pi
// and the repo's config.json are never read or written.
//
// Usage: go run ./tests/e2e
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	modelID   = "mock-e2e-model"
	provider  = "mock"
	keyEnv    = "ROUTRE_E2E_KEY"
	healthTTL = 20 * time.Second
)

// artifactsDir is the source-relative artifact directory, resolved once from
// the module root so the run summary never depends on the working directory.
var artifactsDir = filepath.Join("tests", "e2e", "artifacts")

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: FAIL: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("e2e: PASS")
}

func run() error {
	started := time.Now().UTC()
	art := &artifact{Command: "go run ./tests/e2e", StartedAt: started.Format(time.RFC3339Nano)}

	// Always record the result, pass or fail.
	pass := false
	defer func() { writeArtifact(art, pass, time.Now().UTC()) }()

	root, err := repoRoot()
	if err != nil {
		return err
	}
	artifactsDir = filepath.Join(root, "tests", "e2e", "artifacts")
	tmp, err := os.MkdirTemp("", "routre-e2e-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	home := filepath.Join(tmp, "home")
	cfgPath := filepath.Join(home, ".routre", "config.json")
	piModels := filepath.Join(home, ".pi", "agent", "models.json")
	for _, d := range []string{filepath.Dir(cfgPath), filepath.Dir(piModels)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	// Isolate the Pi models file too: it exists only so nothing can resolve
	// a path under the real $HOME.
	if err := os.WriteFile(piModels, []byte("{}\n"), 0o600); err != nil {
		return err
	}
	art.PiModels = piModels

	mockPort, err := freePort()
	if err != nil {
		return err
	}
	gwPort, err := freePort()
	if err != nil {
		return err
	}
	if err := writeConfig(cfgPath, mockPort, gwPort); err != nil {
		return err
	}
	art.Config = cfgPath

	bin := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		return err
	}
	routreBin := filepath.Join(bin, "routre")
	mockBin := filepath.Join(bin, "mock-upstream")
	if err := build(root, ".", routreBin); err != nil {
		return fmt.Errorf("build routre: %w", err)
	}
	if err := build(root, "./cmd/mock-upstream", mockBin); err != nil {
		return fmt.Errorf("build mock-upstream: %w", err)
	}

	env := childEnv(home, tmp)

	mockLog := &syncBuffer{}
	mock, err := start(env, mockBin, mockLog, "-port", fmt.Sprint(mockPort))
	if err != nil {
		return fmt.Errorf("start mock-upstream: %w", err)
	}
	defer stop(mock)
	gwLog := &syncBuffer{}
	gw, err := start(env, routreBin, gwLog, "serve", "--config", cfgPath, "--port", fmt.Sprintf("127.0.0.1:%d", gwPort))
	if err != nil {
		return fmt.Errorf("start gateway: %w", err)
	}
	defer stop(gw)

	// A signal must not leave orphans behind.
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case s := <-sigc:
			mock.kill()
			gw.kill()
			fmt.Fprintf(os.Stderr, "e2e: interrupted (%v)\n", s)
			os.Exit(1)
		case <-done:
		}
	}()

	base := fmt.Sprintf("http://127.0.0.1:%d", gwPort)
	if err := waitHealthy(base); err != nil {
		art.GatewayStderr = tail(gwLog.String(), 2000)
		return fmt.Errorf("gateway never became healthy: %w", err)
	}

	models, err := getModels(base)
	if err != nil {
		return err
	}
	art.Models = models
	// Model IDs are namespaced "<provider>/<model>" by the gateway.
	routedModel := provider + "/" + modelID
	if !contains(models, routedModel) {
		return fmt.Errorf("model %q missing from /v1/models: %v", routedModel, models)
	}

	listOut, err := run1(routreBin, env, "list", "--config", cfgPath, "--url", base)
	art.RoutreList = listOut
	if err != nil {
		return fmt.Errorf("routre list: %w", err)
	}
	if !strings.Contains(listOut, provider) {
		return fmt.Errorf("routre list does not mention provider %q: %q", provider, listOut)
	}

	status, hdr, body, err := chat(base, routedModel)
	art.RoutedStatus = status
	art.RoutedModel = hdr
	art.GatewayStderr = tail(gwLog.String(), 2000)
	art.UpstreamStderr = tail(mockLog.String(), 2000)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("routed chat status %d: %s", status, body)
	}
	if hdr["X-Llrouter-Provider"] != provider {
		return fmt.Errorf("response did not come from routre: X-Llrouter-Provider=%q body=%s", hdr["X-Llrouter-Provider"], body)
	}

	pass = true
	return nil
}

// repoRoot walks up from the working directory to the module root.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		b, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil && strings.Contains(string(b), "module github.com/mariobgsp/routre") {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("routre module root not found above %s", dir)
		}
		dir = parent
	}
}

func build(dir, pkg, out string) error {
	cmd := exec.Command("go", "build", "-o", out, pkg)
	cmd.Dir = dir
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, tail(string(b), 1500))
	}
	return nil
}

// childEnv is the full environment for every child: the temp HOME, the temp
// CLI data dir, and a throwaway API key.
func childEnv(home, tmp string) []string {
	env := append(os.Environ(),
		"HOME="+home,
		"USERPROFILE="+home, // os.UserHomeDir on windows
		"ROUTRE_CLI_DATA_DIR="+filepath.Join(tmp, "data"),
		keyEnv+"=e2e-test-key",
	)
	return env
}

func writeConfig(path string, mockPort, gwPort int) error {
	cfg := map[string]any{
		"listen":    fmt.Sprintf("127.0.0.1:%d", gwPort),
		"log_level": "info",
		"tiers": []any{map[string]any{
			"name": "e2e",
			"providers": []any{map[string]any{
				"name":        provider,
				"kind":        "openai",
				"base_url":    fmt.Sprintf("http://127.0.0.1:%d", mockPort),
				"api_key_env": keyEnv,
				"models":      []string{modelID},
			}},
		}},
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

type child struct {
	cmd *exec.Cmd
	log *syncBuffer
}

func start(env []string, bin string, log *syncBuffer, args ...string) (*child, error) {
	cmd := exec.Command(bin, args...)
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.Env = env
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &child{cmd: cmd, log: log}, nil
}

// stop kills a child and waits for it, so no process outlives the test.
func (c *child) kill() {
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
}

func stop(c *child) {
	c.kill()
	_ = c.cmd.Wait()
}

func waitHealthy(base string) error {
	deadline := time.Now().Add(healthTTL)
	var last error
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/healthz")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			last = fmt.Errorf("status %d", resp.StatusCode)
		} else {
			last = err
		}
		time.Sleep(100 * time.Millisecond)
	}
	return last
}

func getModels(base string) ([]string, error) {
	resp, err := http.Get(base + "/v1/models")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var doc struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(doc.Data))
	for _, m := range doc.Data {
		out = append(out, m.ID)
	}
	return out, nil
}

func run1(bin string, env []string, args ...string) (string, error) {
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	b, err := cmd.CombinedOutput()
	return string(b), err
}

func chat(base, model string) (int, map[string]string, string, error) {
	body := strings.NewReader(`{"model":"` + model + `","messages":[{"role":"user","content":"ping"}]}`)
	resp, err := http.Post(base+"/v1/chat/completions", "application/json", body)
	if err != nil {
		return 0, nil, "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	hdr := map[string]string{}
	for k := range resp.Header {
		if strings.HasPrefix(strings.ToLower(k), "x-llrouter-") {
			hdr[k] = resp.Header.Get(k)
		}
	}
	return resp.StatusCode, hdr, string(b), nil
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// syncBuffer collects child output written from another goroutine.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type artifact struct {
	Command        string            `json:"command"`
	Config         string            `json:"config"`
	PiModels       string            `json:"pi_models"`
	StartedAt      string            `json:"started_at"`
	FinishedAt     string            `json:"finished_at"`
	DurationMS     int64             `json:"duration_ms"`
	Models         []string          `json:"models"`
	RoutreList     string            `json:"routre_list"`
	RoutedStatus   int               `json:"routed_status"`
	RoutedModel    map[string]string `json:"routed_headers"`
	GatewayStderr  string            `json:"gateway_stderr,omitempty"`
	UpstreamStderr string            `json:"upstream_stderr,omitempty"`
	Result         string            `json:"result"`
}

// writeArtifact stores the run summary atomically (temp file + rename) so a
// killed run never leaves a half-written file behind.
func writeArtifact(a *artifact, pass bool, finished time.Time) {
	if a.StartedAt != "" {
		if t, err := time.Parse(time.RFC3339Nano, a.StartedAt); err == nil {
			a.DurationMS = finished.Sub(t).Milliseconds()
		}
	}
	a.FinishedAt = finished.Format(time.RFC3339Nano)
	a.Result = "fail"
	if pass {
		a.Result = "pass"
	}
	b, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return
	}
	dir := artifactsDir
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, ".last-run-*.json")
	if err != nil {
		return
	}
	name := tmp.Name()
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		os.Remove(name)
		return
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return
	}
	_ = os.Rename(name, filepath.Join(dir, "last-run.json"))
}
