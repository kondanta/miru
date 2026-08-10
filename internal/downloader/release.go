package downloader

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
)

type ghRelease struct {
	TagName string    `json:"tag_name"`
	Assets  []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
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
// returns its tag name and the download URL for the current platform's asset.
// Pass tag="" or tag="latest" to fetch the latest release.
func fetchRelease(
	ctx context.Context,
	client *http.Client,
	baseURL, tag string,
) (tagOut, downloadURL string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releaseEndpoint(baseURL, tag), nil)
	if err != nil {
		return "", "", fmt.Errorf("build release request: %w", err)
	}

	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("fetch release: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("GitHub API: unexpected status %d", resp.StatusCode)
	}

	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", "", fmt.Errorf("decode release: %w", err)
	}

	want, err := assetName(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", "", err
	}

	for _, a := range rel.Assets {
		if a.Name == want {
			return rel.TagName, a.BrowserDownloadURL, nil
		}
	}

	return "", "", fmt.Errorf("release %s has no asset %q for this platform", rel.TagName, want)
}

// downloadBinary fetches the binary at downloadURL and atomically installs it
// at destPath with executable permissions.
func downloadBinary(ctx context.Context, client *http.Client, downloadURL, destPath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return fmt.Errorf("build download request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download binary: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("binary download: unexpected status %d", resp.StatusCode)
	}

	// Temp file in same dir as dest so os.Rename is atomic (same filesystem).
	tmp, err := os.CreateTemp(filepath.Dir(destPath), "yt-dlp-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}

	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }() // no-op after successful Rename; cleans up on any failure path

	if _, err := io.Copy(tmp, resp.Body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write binary: %w", err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return fmt.Errorf("chmod binary: %w", err)
	}

	if err := os.Rename(tmpPath, destPath); err != nil {
		return fmt.Errorf("install binary: %w", err)
	}

	return nil
}
