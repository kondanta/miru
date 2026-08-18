package downloader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// githubReleasesBaseURL is the base GitHub API path for yt-dlp releases.
const githubReleasesBaseURL = "https://api.github.com/repos/yt-dlp/yt-dlp/releases"

// QualityFormats maps user-facing quality strings to yt-dlp format selectors.
// It is the canonical source of valid quality values; the server imports it to
// validate quality fields without duplicating the set.
var QualityFormats = map[string]string{
	"best":  "bestvideo+bestaudio/best",
	"360p":  "bestvideo[height<=360]+bestaudio/best[height<=360]",
	"480p":  "bestvideo[height<=480]+bestaudio/best[height<=480]",
	"720p":  "bestvideo[height<=720]+bestaudio/best[height<=720]",
	"1080p": "bestvideo[height<=1080]+bestaudio/best[height<=1080]",
	"1440p": "bestvideo[height<=1440]+bestaudio/best[height<=1440]",
	"2160p": "bestvideo[height<=2160]+bestaudio/best[height<=2160]",
	"4320p": "bestvideo[height<=4320]+bestaudio/best[height<=4320]",
}

// DownloadOpts configures a single yt-dlp invocation.
type DownloadOpts struct {
	Quality       string
	SponsorBlock  bool
	WriteInfoJSON bool      // set true when the nfo package will consume the output
	Stderr        io.Writer // if nil, stderr is discarded; set to capture error output
	Verbose       bool      // passes --verbose to yt-dlp; use for debugging
}

// Manager owns the yt-dlp binary lifecycle and invocation.
type Manager struct {
	binPath        string
	releaseBaseURL string // GitHub releases base URL; overridable in tests
	pinnedVersion  string // empty or "latest" = always track latest; any other value pins that tag
	httpClient     *http.Client
	log            *slog.Logger
	mu             sync.RWMutex // guards the binary on disk, pinnedVersion, and cookiesFile
	wg             sync.WaitGroup

	cookiesFile string // Netscape-format cookies file path; empty means no cookies
}

// SetCookiesFile sets the path to a Netscape-format cookies file passed to
// every yt-dlp invocation. Safe to call concurrently; takes effect on the next
// download. Pass an empty string to disable cookies.
func (m *Manager) SetCookiesFile(path string) {
	m.mu.Lock()
	m.cookiesFile = path
	m.mu.Unlock()
}

// CookiesFile returns the currently configured cookies file path.
func (m *Manager) CookiesFile() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cookiesFile
}

// New creates a Manager that stores the yt-dlp binary inside dataDir.
// No I/O is performed until EnsureReady is called.
func New(dataDir string, log *slog.Logger) *Manager {
	// Clone DefaultTransport to inherit proxy, HTTP/2, and TLS settings.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 30 * time.Second

	return &Manager{
		binPath:        filepath.Join(dataDir, "yt-dlp"),
		releaseBaseURL: githubReleasesBaseURL,
		httpClient: &http.Client{
			Transport: transport,
			// Reject HTTPS→HTTP redirects to prevent MITM attacks during binary download.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) > 0 && via[0].URL.Scheme == "https" && req.URL.Scheme == "http" {
					return fmt.Errorf("redirect from https to http rejected")
				}
				return nil
			},
		},
		log: log,
	}
}

// Close waits for the background update goroutine to finish.
// Cancel the context passed to EnsureReady before calling Close.
func (m *Manager) Close() {
	m.wg.Wait()
}

// EnsureReady guarantees a working yt-dlp binary is available before returning.
// If no binary is cached it downloads the latest release and fails fast on any
// error — yt-dlp is required for the tool to function.
// After ensuring a binary is available it spawns a goroutine (tied to ctx) that
// checks for and installs updates in the background.
func (m *Manager) EnsureReady(ctx context.Context) error {
	_, err := os.Stat(m.binPath)

	switch {
	case errors.Is(err, os.ErrNotExist):
		m.log.Info("yt-dlp binary not found, downloading latest release")

		ri, ferr := fetchRelease(ctx, m.httpClient, m.releaseBaseURL, "")
		if ferr != nil {
			return fmt.Errorf("fetch yt-dlp release: %w", ferr)
		}

		if ferr := downloadAndInstall(ctx, m.httpClient, ri, m.binPath); ferr != nil {
			return fmt.Errorf("install yt-dlp: %w", ferr)
		}

	case err != nil:
		return fmt.Errorf("stat yt-dlp binary: %w", err)
	}

	if err := m.smokeTest(ctx); err != nil {
		return err
	}

	m.wg.Go(func() { m.checkForUpdate(ctx) })

	return nil
}

// SetVersion installs a specific yt-dlp version and pins the Manager to it.
// Pass "latest" or "" to switch back to tracking the latest release.
// Blocks until the version is installed and smoke-tested.
func (m *Manager) SetVersion(ctx context.Context, tag string) error {
	ri, err := fetchRelease(ctx, m.httpClient, m.releaseBaseURL, tag)
	if err != nil {
		return fmt.Errorf("fetch yt-dlp release %q: %w", tag, err)
	}

	// Download and verify outside the lock: the network transfer must not block
	// concurrent Download calls for its duration.
	tmpPath, err := downloadToTemp(ctx, m.httpClient, ri.downloadURL, filepath.Dir(m.binPath))
	if err != nil {
		return fmt.Errorf("download yt-dlp %q: %w", tag, err)
	}
	defer func() { _ = os.Remove(tmpPath) }() // no-op after successful Rename

	if err := verifyChecksum(ctx, m.httpClient, ri, tmpPath); err != nil {
		return fmt.Errorf("yt-dlp %q checksum: %w", tag, err)
	}

	backup := m.binPath + ".bak"

	m.mu.Lock()
	defer m.mu.Unlock()

	// Rename the existing binary to backup; if there is no binary yet (fresh
	// install), skip the rename and flag that there is nothing to roll back to.
	hasBackup := true
	if renameErr := os.Rename(m.binPath, backup); renameErr != nil {
		if !errors.Is(renameErr, os.ErrNotExist) {
			return fmt.Errorf("back up yt-dlp before version switch: %w", renameErr)
		}
		hasBackup = false
	}

	if err := installBinary(tmpPath, m.binPath); err != nil {
		if hasBackup {
			m.recoverBinary(backup)
		}
		return fmt.Errorf("install yt-dlp %q: %w", tag, err)
	}

	if err := m.smokeTest(ctx); err != nil {
		if hasBackup {
			m.recoverBinary(backup)
		} else if removeErr := os.Remove(m.binPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			m.log.Warn("yt-dlp: could not remove broken binary after smoke-test failure", "err", removeErr)
		}
		return fmt.Errorf("smoke test yt-dlp %q: %w", tag, err)
	}

	if hasBackup {
		_ = os.Remove(backup)
	}
	m.pinnedVersion = tag
	m.log.Info("yt-dlp version set", "version", tag)

	return nil
}

// Download invokes yt-dlp for a single video URL, writing all output lines to
// progress. The caller is responsible for constructing outDir (typically
// config.DownloadsDir/username). Context cancellation aborts the subprocess.
func (m *Manager) Download(
	ctx context.Context, rawURL, outDir string, opts DownloadOpts, progress io.Writer,
) error {
	format, ok := QualityFormats[opts.Quality]
	if !ok {
		return fmt.Errorf("unknown quality %q", opts.Quality)
	}

	if !strings.HasPrefix(rawURL, "https://") && !strings.HasPrefix(rawURL, "http://") {
		return fmt.Errorf("url must use http or https scheme")
	}

	args := []string{
		"--newline",
		"--no-playlist",
		"--extractor-args", "youtube:player_client=android",
		"--format", format,
		"--merge-output-format", "mp4",
		"--output", "%(upload_date>%Y-%m-%d)s - %(title)s.%(ext)s",
		"-P", outDir,
	}

	if opts.SponsorBlock {
		args = append(args, "--sponsorblock-remove", "all")
	}

	if opts.WriteInfoJSON {
		args = append(args, "--write-info-json")
	}

	if opts.Verbose {
		args = append(args, "--verbose")
	}

	m.mu.RLock()
	cookiesFile := m.cookiesFile
	m.mu.RUnlock()
	if cookiesFile != "" {
		args = append(args, "--cookies", cookiesFile)
	}

	// "--" separates yt-dlp flags from the URL so a URL beginning with "-"
	// cannot be parsed as a flag by yt-dlp.
	args = append(args, "--", rawURL)

	// Hold the read lock through Start so that a concurrent update cannot
	// rename the binary between when we capture the path and when the kernel
	// opens the file. Once Start returns the kernel holds the inode open and
	// a subsequent rename is safe.
	stderr := opts.Stderr
	if stderr == nil {
		stderr = io.Discard
	}

	m.log.Debug("yt-dlp invocation", "args", args, "path", os.Getenv("PATH"))

	m.mu.RLock()
	cmd := exec.CommandContext(ctx, m.binPath, args...)
	cmd.Stdout = progress
	cmd.Stderr = stderr
	err := cmd.Start()
	m.mu.RUnlock()

	if err != nil {
		return fmt.Errorf("yt-dlp start: %w", err)
	}

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("yt-dlp: %w", err)
	}

	return nil
}

func (m *Manager) smokeTest(ctx context.Context) error {
	out, err := exec.CommandContext(ctx, m.binPath, "--version").Output()
	if err != nil {
		return fmt.Errorf("yt-dlp smoke test: %w", err)
	}

	m.log.Debug("yt-dlp ready", "version", strings.TrimSpace(string(out)))

	return nil
}

func (m *Manager) checkForUpdate(ctx context.Context) {
	m.mu.RLock()
	version := m.pinnedVersion
	m.mu.RUnlock()

	ri, err := fetchRelease(ctx, m.httpClient, m.releaseBaseURL, version)
	if err != nil {
		m.log.Warn("yt-dlp update check failed", "err", err)
		return
	}

	// Hold RLock through Start (same pattern as Download) to prevent a concurrent
	// SetVersion from renaming the binary while the kernel opens the exec.
	var versionBuf strings.Builder

	versionCmd := exec.CommandContext(ctx, m.binPath, "--version")
	versionCmd.Stdout = &versionBuf

	m.mu.RLock()
	startErr := versionCmd.Start()
	m.mu.RUnlock()

	if startErr != nil {
		m.log.Warn("yt-dlp update check: could not read current version", "err", startErr)
		return
	}

	if err := versionCmd.Wait(); err != nil {
		m.log.Warn("yt-dlp update check: could not read current version", "err", err)
		return
	}

	current := strings.TrimSpace(versionBuf.String())
	if current == ri.tag {
		m.log.Debug("yt-dlp is up to date", "version", ri.tag)
		return
	}

	m.log.Info("updating yt-dlp", "current", current, "latest", ri.tag)

	// Download and verify outside the lock so concurrent Download calls are not blocked.
	tmpPath, err := downloadToTemp(ctx, m.httpClient, ri.downloadURL, filepath.Dir(m.binPath))
	if err != nil {
		m.log.Warn("yt-dlp update: download failed", "err", err)
		return
	}

	defer func() { _ = os.Remove(tmpPath) }()

	if err := verifyChecksum(ctx, m.httpClient, ri, tmpPath); err != nil {
		m.log.Warn("yt-dlp update: checksum failed, skipping update", "err", err)
		return
	}

	backup := m.binPath + ".bak"

	m.mu.Lock()
	defer m.mu.Unlock()

	if err := os.Rename(m.binPath, backup); err != nil {
		m.log.Warn("yt-dlp update: could not back up binary, skipping update", "err", err)
		return
	}

	if err := installBinary(tmpPath, m.binPath); err != nil {
		m.log.Warn("yt-dlp update: install failed, rolling back", "err", err)
		m.recoverBinary(backup)

		return
	}

	if err := m.smokeTest(ctx); err != nil {
		m.log.Warn("yt-dlp update: smoke test failed, rolling back", "err", err)
		m.recoverBinary(backup)

		return
	}

	_ = os.Remove(backup)
	m.log.Info("yt-dlp updated", "version", ri.tag)
}

// recoverBinary restores the backup binary to binPath via rename.
// Called after a failed update while holding m.mu.Lock(). If the rename fails
// the binary is unavailable; the operator must restart the application to retry.
// A re-download is not attempted: if rename fails the filesystem is in a bad
// state and a download to the same path will fail for the same reason.
func (m *Manager) recoverBinary(backup string) {
	if err := os.Rename(backup, m.binPath); err != nil {
		m.log.Error("yt-dlp rollback failed; binary is unavailable; restart to retry", "err", err)
		return
	}

	m.log.Info("yt-dlp rolled back to previous version")
}
