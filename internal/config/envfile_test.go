package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadEnvFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "routre.env")
	content := `# comment line

ANTHROPIC_API_KEY="sk-ant-123"
GLM_API_KEY='glm-key'
IFLOW_API_KEY=plain-key
export QUOTED="a b c"
`
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	// Ensure the keys are not present before.
	os.Unsetenv("ANTHROPIC_API_KEY")
	os.Unsetenv("GLM_API_KEY")
	os.Unsetenv("IFLOW_API_KEY")
	os.Unsetenv("QUOTED")

	if err := LoadEnvFile(p); err != nil {
		t.Fatalf("LoadEnvFile: %v", err)
	}
	for k, want := range map[string]string{
		"ANTHROPIC_API_KEY": "sk-ant-123",
		"GLM_API_KEY":       "glm-key",
		"IFLOW_API_KEY":     "plain-key",
		"QUOTED":            "a b c",
	} {
		if got := os.Getenv(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

func TestLoadEnvFileExistingWins(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "routre.env")
	if err := os.WriteFile(p, []byte("MY_KEY=from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MY_KEY", "from-shell")
	if err := LoadEnvFile(p); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("MY_KEY"); got != "from-shell" {
		t.Fatalf("shell export must win, got %q", got)
	}
}

func TestLoadEnvFileMissingIsNoError(t *testing.T) {
	if err := LoadEnvFile(filepath.Join(t.TempDir(), "nope.env")); err != nil {
		t.Fatalf("missing env file must not error: %v", err)
	}
}

func TestLoadEnvFileMalformed(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "routre.env")
	if err := os.WriteFile(p, []byte("NOEQUALS\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadEnvFile(p); err == nil {
		t.Fatal("malformed line must error")
	}
}
