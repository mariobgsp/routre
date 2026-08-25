// Package keystore holds provider API keys (and later the optional gateway
// auth secret) in memory, sourced from the routre.env key file. It avoids
// mutating the process environment (os.Setenv/os.Unsetenv), so concurrent
// readers elsewhere never observe a torn key state during a rotation.
package keystore

import (
	"sync"

	"github.com/mariobgsp/routre/internal/config"
)

// Store is a concurrency-safe in-memory key registry.
type Store struct {
	mu   sync.Mutex
	keys map[string]string
}

// New returns an empty store.
func New() *Store {
	return &Store{keys: map[string]string{}}
}

// Get returns the value and presence for envName.
func (s *Store) Get(envName string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.keys[envName]
	return v, ok
}

// Set stores envName's value.
func (s *Store) Set(envName, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys[envName] = value
}

// Refresh re-reads the env file and, if envName's value rotated there,
// updates the stored key. It returns the current value and whether it
// changed. It never mutates the process environment.
func (s *Store) Refresh(envFilePath, envName string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	nv, present, err := config.EnvFileValue(envFilePath, envName)
	if err != nil || !present {
		// Unreadable file or key not in the file: the shell/loaded value is
		// authoritative and unchanged.
		return s.keys[envName], false
	}
	if nv == s.keys[envName] {
		return nv, false
	}
	s.keys[envName] = nv
	return nv, true
}
