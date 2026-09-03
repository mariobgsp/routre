// Model auto-discovery: at startup (and on a periodic refresh) each
// provider's own GET {base_url}/models is fetched and merged into its
// candidate set. Explicit `models` in config remain the authoritative seed;
// discovery only ADDS provider model IDs at runtime. If a provider is
// unreachable or refuses, it is skipped with a warning — startup never
// fails over discovery.
package router

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// DiscoveryRefreshInterval is how often model lists are re-fetched while
// serving. Discovery also runs once at startup.
const DiscoveryRefreshInterval = 6 * time.Hour

// modelsResponse is the OpenAI /v1/models envelope.
type modelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

// discoveryHTTPClient returns a keepalive-pooled client for discovery (no
// overall timeout: the per-phase timeouts in the transport bound it).
func discoveryHTTPClient() *http.Client {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
	}
	return &http.Client{Transport: transport}
}

// DiscoverModels fetches each provider's own model list and merges the
// discovered IDs (additively) into that provider's candidate set. A nil
// client uses a fresh keepalive-pooled client. Unreachable/refusing
// providers are skipped; each failure is reported via warn (may be nil).
// Returns the number of providers successfully refreshed.
func (r *Router) DiscoverModels(client *http.Client, warn func(provider string, err error)) int {
	refreshed, _ := r.DiscoverModelsWithStats(client, warn)
	return refreshed
}

// DiscoverModelsWithStats is DiscoverModels plus the count of newly added
// model IDs. It also stamps the last-success timestamp (see
// LastDiscoveryUnix) when at least one provider refreshed.
func (r *Router) DiscoverModelsWithStats(client *http.Client, warn func(provider string, err error)) (refreshed, added int) {
	r.mu.RLock()
	snapshot := make([]*ProviderState, len(r.provs))
	copy(snapshot, r.provs)
	r.mu.RUnlock()

	if client == nil {
		client = discoveryHTTPClient()
	}
	for _, p := range snapshot {
		ids, err := fetchModels(client, p.Provider)
		if err != nil {
			if warn != nil {
				warn(p.Provider.Name, err)
			}
			continue
		}
		refreshed++
		added += r.mergeDiscovered(p, ids)
	}
	if refreshed > 0 {
		r.discTS.Store(time.Now().Unix())
	}
	return refreshed, added
}

// LastDiscoveryUnix returns the unix seconds of the last successful
// discovery run (any provider refreshed), or 0 if never.
func (r *Router) LastDiscoveryUnix() int64 { return r.discTS.Load() }

// fetchModels GETs {base_url}/models (trailing slash tolerant) and returns
// the deduplicated model IDs. The provider API key (from APIKeyEnv) is sent
// as a Bearer token when present.
func fetchModels(client *http.Client, p ProviderInfo) ([]string, error) {
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(p.BaseURL, "/")+"/models", nil)
	if err != nil {
		return nil, err
	}
	if p.APIKeyEnv != "" {
		if key := os.Getenv(p.APIKeyEnv); key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("model discovery: unexpected status %d", resp.StatusCode)
	}
	var mr modelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&mr); err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(mr.Data))
	ids := make([]string, 0, len(mr.Data))
	for _, d := range mr.Data {
		if d.ID == "" {
			continue
		}
		if _, ok := seen[d.ID]; ok {
			continue
		}
		seen[d.ID] = struct{}{}
		ids = append(ids, d.ID)
	}
	return ids, nil
}

// mergeDiscovered adds ids that are not already in p's model list
// (explicit config models are kept as the seed and never shadowed).
// Returns the number of IDs actually added.
func (r *Router) mergeDiscovered(p *ProviderState, ids []string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	existing := make(map[string]struct{}, len(p.Provider.Models))
	for _, m := range p.Provider.Models {
		existing[m] = struct{}{}
	}
	added := 0
	for _, id := range ids {
		if _, ok := existing[id]; ok {
			continue
		}
		p.Provider.Models = append(p.Provider.Models, id)
		existing[id] = struct{}{}
		added++
	}
	return added
}
