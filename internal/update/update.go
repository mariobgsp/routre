// Package update implements routre's self-update: it finds the latest
// GitHub release without touching api.github.com (rate limits), verifies the
// download against the release's checksums.txt, and atomically replaces its
// own binary. Zero external dependencies by design.
package update

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	Owner    = "mariobgsp"
	Repo     = "routre"
	baseHost = "https://github.com"
)

// newClient returns an HTTP client with bounded dial/TLS/header timeouts
// (mirrors internal/proxy's upstream transport) and a generous overall
// timeout for slow release downloads.
func newClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			MaxIdleConns:          4,
			IdleConnTimeout:       90 * time.Second,
		},
		Timeout: 10 * time.Minute,
	}
}

// newNoRedirectClient surfaces 3xx responses instead of following them —
// used to read the tag out of the releases/latest Location header.
func newNoRedirectClient() *http.Client {
	c := newClient()
	c.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return c
}

func newRequest(url string) (*http.Request, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "routre-update")
	return req, nil
}

// Release describes the latest published release.
type Release struct {
	Tag      string // e.g. "v0.4.0"
	AssetURL string // tarball redirect URL
	Checksum struct {
		URL  string
		Want string // expected sha256 of AssetURL's payload
	}
}

func assetName() string {
	ext := ".tar.gz"
	if runtime.GOOS == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("routre_%s_%s%s", runtime.GOOS, runtime.GOARCH, ext)
}

// Latest resolves the newest release via the releases/latest 302 redirect
// (never api.github.com: unauthenticated there is 60 req/h/IP).
func Latest() (*Release, error) {
	client := newNoRedirectClient()
	asset := assetName()
	url := fmt.Sprintf("%s/%s/%s/releases/latest/download/%s", baseHost, Owner, Repo, asset)

	req, err := newRequest(url)
	if err != nil {
		return nil, err
	}
	// Do not follow the redirect; the Location header carries the tag.
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("resolve latest release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 300 || resp.StatusCode > 399 {
		return nil, fmt.Errorf("resolve latest release: unexpected status %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	tag := tagFromLocation(loc)
	if tag == "" {
		return nil, fmt.Errorf("could not determine latest tag from redirect %q", loc)
	}
	if resp.StatusCode == 404 {
		return nil, fmt.Errorf("no published release found (%s)", url)
	}
	r := &Release{Tag: tag}
	downloadBase := fmt.Sprintf("%s/%s/%s/releases/download/%s", baseHost, Owner, Repo, tag)
	r.AssetURL = downloadBase + "/" + asset
	r.Checksum.URL = downloadBase + "/checksums.txt"
	return r, nil
}

// tagFromLocation extracts "vX.Y.Z" from a .../releases/tag/vX.Y.Z or
// .../releases/download/vX.Y.Z/... redirect URL.
func tagFromLocation(loc string) string {
	for _, marker := range []string{"/releases/tag/", "/releases/download/"} {
		i := strings.LastIndex(loc, marker)
		if i < 0 {
			continue
		}
		tail := loc[i+len(marker):]
		// download-URLs continue past the tag with /asset-name — keep only
		// the first path segment. Keep the raw tag (incl. leading "v"):
		// release asset URLs need it verbatim.
		if end := strings.Index(tail, "/"); end >= 0 {
			tail = tail[:end]
		}
		return tail
	}
	return ""
}

// Checksum fetches the release checksums.txt and returns the hash recorded
// for the given asset name.
func (r *Release) checksumFor(asset string) (string, error) {
	client := newClient()
	resp, err := client.Get(r.Checksum.URL)
	if err != nil {
		return "", fmt.Errorf("fetch %s: %w", r.Checksum.URL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("fetch checksums: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(body), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[1] == asset {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("no checksum for %s in checksums.txt", asset)
}

// Apply downloads the release asset into dst (the directory holding the
// current binary), verifies it, and atomically replaces currentPath.
// It returns the version that was installed.
func (r *Release) Apply(currentPath string) error {
	if runtime.GOOS == "windows" {
		// Replacing a running .exe needs a rename-swap dance; deferred.
		return fmt.Errorf("self-update on Windows is not supported yet — " +
			"download the .zip from https://github.com/" + Owner + "/" + Repo + "/releases/latest")
	}
	client := newClient()

	want, err := r.checksumFor(assetName())
	if err != nil {
		return err
	}

	resp, err := client.Get(r.AssetURL)
	if err != nil {
		return fmt.Errorf("download %s: %w", r.AssetURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("download: status %d", resp.StatusCode)
	}

	// Stream to temp file while hashing: one pass, bounded memory.
	tmp, err := os.CreateTemp(filepath.Dir(currentPath), ".routre-update-*.tar.gz")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after successful rename

	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(resp.Body, 1<<30)); err != nil {
		tmp.Close()
		return fmt.Errorf("write download: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != want {
		return fmt.Errorf("checksum mismatch: want %s got %s (binary left untouched)", want, got)
	}

	// Extract the single root-level routre entry from the tarball.
	binBytes, err := extractBinary(tmpName)
	if err != nil {
		return err
	}
	newBin := tmpName + ".new"
	if err := os.WriteFile(newBin, binBytes, 0o755); err != nil {
		return err
	}

	// Atomic replace. Same-directory rename is atomic on unix.
	old := currentPath + ".old"
	_ = os.Remove(old)
	if err := os.Rename(currentPath, old); err != nil {
		os.Remove(newBin)
		return fmt.Errorf("swap out current binary: %w", err)
	}
	if err := os.Rename(newBin, currentPath); err != nil {
		// Roll back so the CLI stays usable.
		_ = os.Rename(old, currentPath)
		os.Remove(newBin)
		return fmt.Errorf("install new binary: %w (previous version restored)", err)
	}
	_ = os.Remove(old)
	return nil
}

// extractBinary pulls the routre entry out of a .tar.gz archive.
func extractBinary(tarGz string) ([]byte, error) {
	f, err := os.Open(tarGz)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("no routre entry in archive")
		}
		if err != nil {
			return nil, err
		}
		name := filepath.Base(hdr.Name)
		if name == "routre" || name == "routre.exe" {
			return io.ReadAll(io.LimitReader(tr, 1<<30))
		}
	}
}

// CompareVersions returns -1 when cur < next, 0 when equal, 1 when
// cur > next. Non-numeric suffixes are ignored ("v0.4.0-rc1" ≈ v0.4.0).
// A value that parses to nothing sorts as oldest (-1 vs any real version).
func CompareVersions(cur, next string) int {
	cs := fields(cur)
	ns := fields(next)
	for i := 0; i < 3; i++ {
		c, n := at(cs, i), at(ns, i)
		switch {
		case c < n:
			return -1
		case c > n:
			return 1
		}
	}
	return 0
}

// fields splits "v1.2.3-beta" → [1 2 3].
func fields(v string) []int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	var out []int
	for _, part := range strings.SplitN(v, ".", 3) {
		n := 0
		for _, r := range part {
			if r < '0' || r > '9' {
				break
			}
			n = n*10 + int(r-'0')
		}
		out = append(out, n)
	}
	return out
}

func at(xs []int, i int) int {
	if i < len(xs) {
		return xs[i]
	}
	return 0
}
