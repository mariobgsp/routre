package keystore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSetAndGet(t *testing.T) {
	s := New()
	s.Set("A", "v1")
	if v, ok := s.Get("A"); !ok || v != "v1" {
		t.Fatalf("Get(A) = %q, %v; want v1, true", v, ok)
	}
	if _, ok := s.Get("MISSING"); ok {
		t.Fatal("Get(MISSING) should be absent")
	}
}

func TestRefreshDetectsRotation(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, "routre.env")
	write := func(v string) {
		if err := os.WriteFile(envPath, []byte("MY_KEY="+v+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s := New()
	s.Set("MY_KEY", "old")
	write("new")
	nv, changed := s.Refresh(envPath, "MY_KEY")
	if !changed {
		t.Fatal("expected rotation detected")
	}
	if nv != "new" {
		t.Fatalf("new value = %q", nv)
	}
	if v, _ := s.Get("MY_KEY"); v != "new" {
		t.Fatalf("stored value = %q", v)
	}
}

func TestRefreshNoChange(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, "routre.env")
	if err := os.WriteFile(envPath, []byte("K=same\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := New()
	s.Set("K", "same")
	if _, changed := s.Refresh(envPath, "K"); changed {
		t.Fatal("no rotation should report changed=false")
	}
}

func TestRefreshKeyMissingInFileKeepsValue(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, "routre.env")
	if err := os.WriteFile(envPath, []byte("OTHER=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := New()
	s.Set("K", "shell-value")
	nv, changed := s.Refresh(envPath, "K")
	if changed {
		t.Fatal("key absent from file should not report changed")
	}
	if nv != "shell-value" {
		t.Fatalf("should keep the authoritative value, got %q", nv)
	}
}

func TestRefreshNoEnvMutation(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, "routre.env")
	if err := os.WriteFile(envPath, []byte("MUTATE_KEY=fileval\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Setenv("MUTATE_KEY", "shellval")
	defer os.Unsetenv("MUTATE_KEY")
	s := New()
	s.Set("MUTATE_KEY", "shellval")
	// Refresh re-reads the file but must NOT touch the process env.
	_, _ = s.Refresh(envPath, "MUTATE_KEY")
	if got := os.Getenv("MUTATE_KEY"); got != "shellval" {
		t.Fatalf("process env was mutated to %q", got)
	}
}

func TestConcurrentRefreshNoRace(t *testing.T) {
	s := New()
	s.Set("K", "init")
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() { s.Refresh("/nonexistent/env", "K"); done <- struct{}{} }()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}
