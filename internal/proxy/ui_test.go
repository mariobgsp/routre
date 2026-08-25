package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/mariobgsp/routre/internal/cache"
	"github.com/mariobgsp/routre/internal/config"
	"github.com/mariobgsp/routre/internal/router"
	"github.com/mariobgsp/routre/internal/rtk"
	"github.com/mariobgsp/routre/internal/usage"
	"log"
)

func uiTestEnv(t *testing.T, cfgJSON string) (base string, store *config.Store, cleanup func()) {
	t.Helper()
	cfgPath := writeConfigFile(t, cfgJSON)
	st := config.NewStore(cfgPath)
	if err := st.Load(); err != nil {
		t.Fatalf("config load: %v", err)
	}
	cfg := st.Get()
	rtr := router.New(tiersFromConfig(cfg), router.DefaultCooldownPolicy())
	rtr.SetForwardUnknown(cfg.ForwardUnknown)
	cch := cache.New(cache.Config{Enabled: cfg.Cache.Enabled, MaxEntries: cfg.Cache.MaxEntries, TTLSeconds: cfg.Cache.TTLSeconds, PrefixOrder: cfg.Cache.PrefixOrder})
	tk := rtk.New(rtk.Config{Enabled: cfg.RTK.Enabled, MinBytes: cfg.RTK.MinBytes, MaxBytes: cfg.RTK.MaxBytes})
	logger := log.New(io.Discard, "", 0)
	h := NewHandlers(st, rtr, cch, tk, logger, usage.New(""))
	srv := New(h, logger)
	ln, err := srv.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Shutdown(2 * 1e9) })
	return "http://" + ln.Addr().String(), st, func() {}
}

func TestUIDashboardLoopback(t *testing.T) {
	base, _, _ := uiTestEnv(t, `{"listen":"127.0.0.1:0","tiers":[]}`)
	// loopback Host should succeed
	resp, err := http.Get(base + "/ui")
	if err != nil {
		t.Fatalf("GET /ui: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET /ui status = %d want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("Local Settings")) {
		t.Fatalf("dashboard missing title: %q", body[:500])
	}
}

func TestUIDashboardRejectsNonLoopbackHost(t *testing.T) {
	base, _, _ := uiTestEnv(t, `{"listen":"127.0.0.1:0","tiers":[]}`)
	req, _ := http.NewRequest("GET", base+"/ui", nil)
	req.Host = "attacker.com"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("non-loopback Host should be 403, got %d", resp.StatusCode)
	}
}

func TestUIConfigJSON(t *testing.T) {
	base, _, _ := uiTestEnv(t, `{"listen":"127.0.0.1:0","tiers":[]}`)
	resp, err := http.Get(base + "/ui/api/config")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var cfg config.Config
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cfg.Listen != "127.0.0.1:0" {
		t.Fatalf("listen = %q", cfg.Listen)
	}
}

func TestUISaveValidAndInvalid(t *testing.T) {
	base, store, _ := uiTestEnv(t, `{"listen":"127.0.0.1:0","tiers":[]}`)
	// valid save — add a tier
	newCfg := config.Config{
		Listen: "127.0.0.1:0",
		Tiers:  []config.Tier{{Name: "t1", Providers: []config.Provider{{Name: "p1", Kind: config.KindOpenAI, BaseURL: "https://example.com", APIKeyEnv: "K1", Models: []string{"m1"}}}}},
		RTK:    config.RTKConfig{Enabled: true, MinBytes: 0, MaxBytes: 10485760},
		Cache:  config.CacheConfig{Enabled: true, MaxEntries: 512, TTLSeconds: 3600},
	}
	b, _ := json.Marshal(newCfg)
	resp, err := http.Post(base+"/ui/api/save", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("save valid: status %d", resp.StatusCode)
	}
	if len(store.Get().Tiers) != 1 || store.Get().Tiers[0].Name != "t1" {
		t.Fatalf("store not updated: %+v", store.Get().Tiers)
	}
	// invalid JSON should be 400 and keep previous config
	resp2, _ := http.Post(base+"/ui/api/save", "application/json", bytes.NewReader([]byte(`{bad`)))
	resp2.Body.Close()
	if resp2.StatusCode != 400 {
		t.Fatalf("invalid JSON should be 400, got %d", resp2.StatusCode)
	}
	if len(store.Get().Tiers) != 1 {
		t.Fatalf("invalid save should keep previous config")
	}
}

func TestUISetEnvAndPersist(t *testing.T) {
	base, store, _ := uiTestEnv(t, `{"listen":"127.0.0.1:0","tiers":[]}`)
	envPath := config.EnvFilePath(store.Path())
	// ensure env file starts absent
	os.Remove(envPath)
	payload, _ := json.Marshal(map[string]string{"key": "TEST_UI_KEY", "value": "secret123"})
	resp, err := http.Post(base+"/ui/api/env", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("set env status %d", resp.StatusCode)
	}
	// check file on disk
	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("env file not written: %v", err)
	}
	if !bytes.Contains(data, []byte("TEST_UI_KEY=secret123")) {
		t.Fatalf("env file content %q", data)
	}
	// check mode 0600
	fi, _ := os.Stat(envPath)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("env file perms %o want 600", fi.Mode().Perm())
	}
}

func TestUISaveRejectsNonLoopbackOrigin(t *testing.T) {
	base, _, _ := uiTestEnv(t, `{"listen":"127.0.0.1:0","tiers":[]}`)
	b, _ := json.Marshal(config.Config{Listen: "127.0.0.1:0"})
	req, _ := http.NewRequest("POST", base+"/ui/api/save", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://attacker.com")
	req.Host = "127.0.0.1:" + base[len(base)-5:] // keep loopback Host but bad Origin
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("bad Origin should be 403, got %d", resp.StatusCode)
	}
}

func TestConfigSaveAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	store := config.NewStore(path)
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Listen: "127.0.0.1:20128", Tiers: []config.Tier{{Name: "t", Providers: []config.Provider{{Name: "p", Kind: config.KindOpenAI, BaseURL: "https://x", APIKeyEnv: "K", Models: []string{"m"}}}}}}
	if err := store.Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got := store.Get().Listen; got != "127.0.0.1:20128" {
		t.Fatalf("listen after save = %q", got)
	}
	// invalid save should not clobber file
	bad := cfg
	bad.Tiers[0].Providers[0].Kind = "invalid"
	if err := store.Save(bad); err == nil {
		t.Fatal("invalid Save should error")
	}
	if got := store.Get().Listen; got != "127.0.0.1:20128" {
		t.Fatalf("after bad Save, listen changed to %q", got)
	}
}

func TestSetEnvFileValue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "routre.env")
	if err := config.SetEnvFileValue(path, "A", "1"); err != nil {
		t.Fatal(err)
	}
	if err := config.SetEnvFileValue(path, "B", "2"); err != nil {
		t.Fatal(err)
	}
	if err := config.SetEnvFileValue(path, "A", "3"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !bytes.Contains(data, []byte("A=3")) || !bytes.Contains(data, []byte("B=2")) {
		t.Fatalf("env file %q", data)
	}
	// check that first A was overwritten, not duplicated
	if bytes.Count(data, []byte("A=")) != 1 {
		t.Fatalf("duplicate A in %q", data)
	}
}
