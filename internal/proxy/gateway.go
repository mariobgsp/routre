package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

// Gateway is the deep module for CLI surface that hides config/key/gateway plumbing.
// It owns config loading, key lookup, and live gateway probing behind a small interface.
type Gateway struct {
	cfgPath string
	baseURL string
	client  *http.Client
}

// NewGateway creates a Gateway for the given config path and base URL.
func NewGateway(cfgPath, baseURL string) *Gateway {
	return &Gateway{cfgPath: cfgPath, baseURL: baseURL, client: &http.Client{}}
}

// Status returns the live gateway status from /v1/status, or an error if not reachable.
func (g *Gateway) Status() (map[string]any, error) {
	return g.fetchJSON(g.baseURL + "/v1/status")
}

// Usage returns the live usage rows from /v1/usage.
func (g *Gateway) Usage() ([]map[string]any, error) {
	data, err := g.fetchJSON(g.baseURL + "/v1/usage")
	if err != nil {
		return nil, err
	}
	rows, _ := data["rows"].([]any)
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		if m, ok := r.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out, nil
}

// Models returns the list of models from /v1/models.
func (g *Gateway) Models() ([]string, error) {
	data, err := g.fetchJSON(g.baseURL + "/v1/models")
	if err != nil {
		return nil, err
	}
	list, _ := data["data"].([]any)
	out := make([]string, 0, len(list))
	for _, m := range list {
		if mm, ok := m.(map[string]any); ok {
			if id, ok := mm["id"].(string); ok {
				out = append(out, id)
			}
		}
	}
	return out, nil
}

func (g *Gateway) fetchJSON(url string) (map[string]any, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	// Attach process token if present (for auth-enabled gateways)
	if tok, err := os.ReadFile(os.ExpandEnv("$HOME/.routre/auth.tok")); err == nil {
		req.Header.Set("X-Routre-Key", string(tok))
		req.Header.Set("Authorization", "Bearer "+string(tok))
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("gateway %d: %s", resp.StatusCode, string(body))
	}
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	return data, nil
}
