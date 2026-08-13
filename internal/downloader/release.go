package downloader

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type ghRelease struct {
	TagName string    `json:"tag_name"`
	Assets  []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// releaseInfo holds the metadata for a single yt-dlp release.
type releaseInfo struct {
	tag         string // e.g. "2024.01.15"
	assetName   string // platform-specific binary name, e.g. "yt-dlp"
	downloadURL string
	checksumURL string // URL of SHA2-256SUMS; empty when the release does not publish one
}

// platformToAsset maps "goos/goarch" to the yt-dlp GitHub release asset name.
// To add a platform, open a PR adding an entry here.
var platformToAsset = map[string]string{
	"linux/amd64": "yt-dlp",
	"linux/arm64": "yt-dlp_linux_aarch64",
	"linux/arm":   "yt-dlp_linux_armv7l",
}

func assetName(goos, goarch string) (string, error) {
	name, ok := platformToAsset[goos+"/"+goarch]
	if !ok {
		return "", fmt.Errorf(
			"unsupported platform %s/%s: open an issue at https://github.com/kondanta/miru",
			goos,
			goarch,
		)
	}

	return name, nil
}

// releaseEndpoint constructs the GitHub releases API URL for the given tag.
// An empty tag or "latest" resolves to the latest release endpoint.
func releaseEndpoint(baseURL, tag string) string {
	if tag == "" || tag == "latest" {
		return baseURL + "/latest"
	}

	return baseURL + "/tags/" + tag
}

// fetchRelease fetches the yt-dlp release identified by tag from baseURL and
// returns a releaseInfo with download and checksum URLs for the current platform.
// Pass tag="" or tag="latest" to fetch the latest release.
func fetchRelease(ctx context.Context, client *http.Client, baseURL, tag string) (releaseInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releaseEndpoint(baseURL, tag), nil)
	if err != nil {
		return releaseInfo{}, fmt.Errorf("build release request: %w", err)
	}

	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return releaseInfo{}, fmt.Errorf("fetch release: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return releaseInfo{}, fmt.Errorf("GitHub API: unexpected status %d", resp.StatusCode)
	}

	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return releaseInfo{}, fmt.Errorf("decode release: %w", err)
	}

	want, err := assetName(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return releaseInfo{}, err
	}

	ri := releaseInfo{tag: rel.TagName, assetName: want}

	for _, a := range rel.Assets {
		switch a.Name {
		case want:
			ri.downloadURL = a.BrowserDownloadURL
		case "SHA2-256SUMS":
			ri.checksumURL = a.BrowserDownloadURL
		}
	}

	if ri.downloadURL == "" {
		return releaseInfo{}, fmt.Errorf("release %s has no asset %q for this platform", rel.TagName, want)
	}

	return ri, nil
}

// downloadToTemp downloads from url to a new temp file inside dir and returns
// its path. The caller must remove the file when done (os.Remove).
func downloadToTemp(ctx context.Context, client *http.Client, url, dir string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("build download request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download binary: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("binary download: unexpected status %d", resp.StatusCode)
	}

	tmp, err := os.CreateTemp(dir, "yt-dlp-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}

	tmpPath := tmp.Name()

	if _, err := io.Copy(tmp, resp.Body); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("write binary: %w", err)
	}

	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("close temp file: %w", err)
	}

	return tmpPath, nil
}

// verifyChecksum downloads SHA2-256SUMS from ri.checksumURL, finds the expected
// hash for ri.assetName, and verifies it against the file at tmpPath.
// Returns nil immediately when ri.checksumURL is empty.
func verifyChecksum(ctx context.Context, client *http.Client, ri releaseInfo, tmpPath string) error {
	if ri.checksumURL == "" {
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ri.checksumURL, nil)
	if err != nil {
		return fmt.Errorf("build checksum request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch checksums: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("checksums: unexpected status %d", resp.StatusCode)
	}

	// SHA2-256SUMS format: "<hash>  <filename>" (two spaces) per line.
	var expected string

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && fields[1] == ri.assetName {
			expected = fields[0]
			break
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read checksums: %w", err)
	}

	if expected == "" {
		return fmt.Errorf("SHA2-256SUMS has no entry for %q", ri.assetName)
	}

	f, err := os.Open(tmpPath)
	if err != nil {
		return fmt.Errorf("open binary for checksum: %w", err)
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("compute SHA256: %w", err)
	}

	if got := hex.EncodeToString(h.Sum(nil)); got != expected {
		return fmt.Errorf("SHA256 mismatch: got %s, want %s", got, expected)
	}

	return nil
}

// installBinary makes tmpPath executable and atomically renames it to destPath.
// tmpPath and destPath must be on the same filesystem for the rename to be atomic.
func installBinary(tmpPath, destPath string) error {
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return fmt.Errorf("chmod binary: %w", err)
	}

	if err := os.Rename(tmpPath, destPath); err != nil {
		return fmt.Errorf("install binary: %w", err)
	}

	return nil
}

// downloadAndInstall downloads, verifies, and atomically installs ri at destPath
// in a single call. Used by EnsureReady (fresh install) and recoverBinary (emergency).
func downloadAndInstall(ctx context.Context, client *http.Client, ri releaseInfo, destPath string) error {
	tmpPath, err := downloadToTemp(ctx, client, ri.downloadURL, filepath.Dir(destPath))
	if err != nil {
		return err
	}

	defer func() { _ = os.Remove(tmpPath) }() // no-op after successful Rename; cleans up on failure

	if err := verifyChecksum(ctx, client, ri, tmpPath); err != nil {
		return fmt.Errorf("checksum: %w", err)
	}

	return installBinary(tmpPath, destPath)
}
