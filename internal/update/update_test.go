package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeTarGz builds a .tar.gz containing one entry "routre" with payload.
func makeTarGz(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name: "routre", Mode: 0o755, Size: int64(len(payload)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sha256hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// serveAssets spins an httptest server exposing /asset and /checksums.txt
// where the recorded checksum may be deliberately wrong.
func serveAssets(t *testing.T, assetName string, assetBytes []byte, corruptChecksum bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/"+assetName, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(assetBytes)
	})
	checksum := fmt.Sprintf("%s  %s\n", sha256hex(assetBytes), assetName)
	if corruptChecksum {
		checksum = strings.Repeat("0", 64) + "  " + assetName + "\n"
	}
	mux.HandleFunc("/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(checksum))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestApplyReplacesBinaryAtomically(t *testing.T) {
	payload := []byte("#!/bin/sh\necho new-binary\n")
	asset := assetName()
	tarball := makeTarGz(t, payload)
	srv := serveAssets(t, asset, tarball, false)

	dir := t.TempDir()
	current := filepath.Join(dir, "routre")
	if err := os.WriteFile(current, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	r := &Release{
		Tag:      "v9.9.9",
		AssetURL: srv.URL + "/" + asset,
	}
	r.Checksum.URL = srv.URL + "/checksums.txt"

	if err := r.Apply(current); err != nil {
		t.Fatalf("apply: %v", err)
	}

	got, err := os.ReadFile(current)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("binary not replaced: got %q", got)
	}
	info, err := os.Stat(current)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("perm = %v, want 0755", info.Mode().Perm())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "routre" {
			t.Fatalf("leftover artifact from update: %s", e.Name())
		}
	}
}

// A checksum mismatch must abort BEFORE anything is replaced: the old
// binary stays byte-identical and usable.
func TestApplyAbortsOnChecksumMismatch(t *testing.T) {
	payload := []byte("new")
	asset := assetName()
	srv := serveAssets(t, asset, payload, true)

	dir := t.TempDir()
	current := filepath.Join(dir, "routre")
	oldContent := []byte("old-binary-content")
	if err := os.WriteFile(current, oldContent, 0o755); err != nil {
		t.Fatal(err)
	}

	r := &Release{AssetURL: srv.URL + "/" + asset}
	r.Checksum.URL = srv.URL + "/checksums.txt"

	err := r.Apply(current)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum mismatch error, got %v", err)
	}
	got, _ := os.ReadFile(current)
	if !bytes.Equal(got, oldContent) {
		t.Fatalf("old binary was disturbed on failed update: %q", got)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "routre" {
			t.Fatalf("failed update left artifacts behind: %s", e.Name())
		}
	}
}

func TestExtractBinaryMissingEntry(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "empty.tar.gz")
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "other-file", Size: 2}); err != nil {
		t.Fatal(err)
	}
	_, _ = tw.Write([]byte("hi"))
	_ = tw.Close()
	_ = gz.Close()
	os.WriteFile(p, buf.Bytes(), 0o644)

	if _, err := extractBinary(p); err == nil {
		t.Fatal("expected error when archive has no routre entry")
	}
}

func TestTagFromLocation(t *testing.T) {
	cases := map[string]string{
		"https://github.com/o/r/releases/tag/v1.2.3":                          "v1.2.3",
		"https://github.com/o/r/releases/download/v1.2.3/routre_linux.tar.gz": "v1.2.3",
		"https://github.com/o/r/releases/download/0.4.0/x.zip":                "0.4.0",
		"https://github.com/o/r/other":                                        "",
	}
	for loc, want := range cases {
		if got := tagFromLocation(loc); got != want {
			t.Errorf("tagFromLocation(%q) = %q, want %q", loc, got, want)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		cur, next string
		want      int
	}{
		{"0.3.1", "v0.3.2", -1},
		{"v0.3.2", "0.3.2", 0},
		{"0.4.0", "v0.3.2", 1},
		{"dev", "v0.3.2", -1},
		{"0.10.0", "v0.9.9", 1}, // numeric, not lexicographic
		{"", "v0.1.0", -1},
	}
	for _, c := range cases {
		if got := CompareVersions(c.cur, c.next); got != c.want {
			t.Errorf("CompareVersions(%q,%q) = %d, want %d", c.cur, c.next, got, c.want)
		}
	}
}
