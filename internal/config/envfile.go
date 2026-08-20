package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// envFileName is the key file routre-cli looks for next to the config file
// (config.json + routre-cli.env in the same directory). It is loaded
// automatically by Load/Reload so users never need shell exports.
const envFileName = "routre-cli.env"

// LoadEnvFile reads a simple KEY=VALUE file and sets each key in the process
// environment — but only if it is not already set, so explicit shell exports
// always win. Syntax: one assignment per line, '#' comments, blank lines
// skipped, optional single/double quotes around the value, no export prefix.
//
// Missing file is not an error (a config without keys is valid; `check`
// will report which keys are missing).
func LoadEnvFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("env: read %s: %w", path, err)
	}
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	line := 0
	for sc.Scan() {
		line++
		raw := strings.TrimSpace(sc.Text())
		if raw == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		raw = strings.TrimPrefix(raw, "export ")
		eq := strings.Index(raw, "=")
		if eq <= 0 {
			return fmt.Errorf("env: %s:%d: expected KEY=VALUE", path, line)
		}
		key := strings.TrimSpace(raw[:eq])
		val := strings.TrimSpace(raw[eq+1:])
		if len(val) >= 2 {
			if (val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'') {
				val = val[1 : len(val)-1]
			}
		}
		if key == "" {
			return fmt.Errorf("env: %s:%d: empty key", path, line)
		}
		if _, present := os.LookupEnv(key); !present {
			if err := os.Setenv(key, val); err != nil {
				return fmt.Errorf("env: %s:%d: %w", path, line, err)
			}
		}
	}
	return sc.Err()
}

// EnvFilePath returns the default key-file path next to the config file.
func EnvFilePath(cfgPath string) string {
	return filepath.Join(filepath.Dir(cfgPath), envFileName)
}

// EnvFileValue reads a single key from the env file at path WITHOUT touching
// the process environment (unlike LoadEnvFile). It returns the value and
// whether the key is present. Missing file or key => ("" , false, nil).
func EnvFileValue(path, key string) (string, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("env: read %s: %w", path, err)
	}
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	for sc.Scan() {
		raw := strings.TrimSpace(sc.Text())
		if raw == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		raw = strings.TrimPrefix(raw, "export ")
		eq := strings.Index(raw, "=")
		if eq <= 0 {
			continue
		}
		k := strings.TrimSpace(raw[:eq])
		if k != key {
			continue
		}
		val := strings.TrimSpace(raw[eq+1:])
		if len(val) >= 2 {
			if (val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'') {
				val = val[1 : len(val)-1]
			}
		}
		return val, true, nil
	}
	return "", false, sc.Err()
}
