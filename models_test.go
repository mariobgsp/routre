package main

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/mariobgsp/routre/internal/config"
)

// provSpec is one provider in the test config fixture.
type provSpec struct {
	name   string
	models []string
}

// writeModelConfig writes a minimal valid config with one tier holding provs and
// returns the loaded store plus the config path.
func writeModelConfig(t *testing.T, provs ...provSpec) (*config.Store, string) {
	t.Helper()
	ps := make([]map[string]any, 0, len(provs))
	for _, p := range provs {
		ps = append(ps, map[string]any{
			"name":        p.name,
			"kind":        "openai",
			"base_url":    "http://127.0.0.1:1/v1",
			"api_key_env": "KEY",
			"models":      p.models,
		})
	}
	raw, err := json.Marshal(map[string]any{
		"listen": "127.0.0.1:1",
		"tiers":  []any{map[string]any{"name": "t1", "providers": ps}},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	st := config.NewStore(path)
	if err := st.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	return st, path
}

// readConfigModels parses the config file at path into provider name → model IDs.
func readConfigModels(t *testing.T, path string) map[string][]string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Tiers []struct {
			Providers []struct {
				Name   string   `json:"name"`
				Models []string `json:"models"`
			} `json:"providers"`
		} `json:"tiers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	out := map[string][]string{}
	for _, tr := range doc.Tiers {
		for _, p := range tr.Providers {
			out[p.Name] = p.Models
		}
	}
	return out
}

func TestPersistDiscoveredModels(t *testing.T) {
	cases := []struct {
		name   string
		config []provSpec
		live   map[string][]string
		want   map[string][]string
		wantN  int
	}{
		{
			name:   "adds new ids, keeps existing order",
			config: []provSpec{{"a", []string{"m"}}},
			live:   map[string][]string{"a": {"m", "m2", "m1"}},
			want:   map[string][]string{"a": {"m", "m1", "m2"}},
			wantN:  2,
		},
		{
			name:   "never prunes",
			config: []provSpec{{"a", []string{"m1", "m2"}}},
			live:   map[string][]string{"a": {"m1"}},
			want:   map[string][]string{"a": {"m1", "m2"}},
			wantN:  0,
		},
		{
			name:   "provider absent from live map untouched",
			config: []provSpec{{"a", []string{"m"}}, {"b", []string{"x"}}},
			live:   map[string][]string{"a": {"m", "mz"}},
			want:   map[string][]string{"a": {"m", "mz"}, "b": {"x"}},
			wantN:  1,
		},
		{
			name:   "no new ids writes nothing",
			config: []provSpec{{"a", []string{"m"}}},
			live:   map[string][]string{"a": {"m"}},
			want:   map[string][]string{"a": {"m"}},
			wantN:  0,
		},
		{
			name:   "multiple providers",
			config: []provSpec{{"a", []string{"m"}}, {"b", []string{"x"}}},
			live:   map[string][]string{"a": {"m", "m2"}, "b": {"x", "x2"}},
			want:   map[string][]string{"a": {"m", "m2"}, "b": {"x", "x2"}},
			wantN:  2,
		},
		{
			name:   "live-only provider is ignored",
			config: []provSpec{{"a", []string{"m"}}},
			live:   map[string][]string{"ghost": {"g"}},
			want:   map[string][]string{"a": {"m"}},
			wantN:  0,
		},
		{
			name:   "store reflects merged config after save",
			config: []provSpec{{"a", []string{"m"}}},
			live:   map[string][]string{"a": {"m", "m2"}},
			want:   map[string][]string{"a": {"m", "m2"}},
			wantN:  1,
		},
	}
	logger := log.New(io.Discard, "", 0)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, path := writeModelConfig(t, tc.config...)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if n := persistDiscoveredModels(st, tc.live, logger); n != tc.wantN {
				t.Fatalf("persistDiscoveredModels = %d, want %d", n, tc.wantN)
			}
			if tc.wantN == 0 {
				after, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(before, after) {
					t.Fatalf("file changed on a no-op persist:\n%s\n%s", before, after)
				}
			}
			if got := readConfigModels(t, path); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("file models = %v, want %v", got, tc.want)
			}
			// Save re-reads the file, so the store must show the same merged set.
			live := map[string][]string{}
			for _, tr := range st.Get().Tiers {
				for _, p := range tr.Providers {
					live[p.Name] = p.Models
				}
			}
			if !reflect.DeepEqual(live, tc.want) {
				t.Fatalf("store models = %v, want %v", live, tc.want)
			}
		})
	}
}
