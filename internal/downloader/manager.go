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

// qualityFormats maps user-facing quality strings to yt-dlp format selectors.
var qualityFormats = map[string]string{
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
	WriteInfoJSON bool // set true when the nfo package will consume the output
}

// Manager owns the yt-dlp binary lifecycle and invocation.
type Manager struct {
	binPath        string
	releaseBaseURL string // GitHub releases base URL; overridable in tests
	pinnedVersion  string // empty or "latest" = always track latest; any other value pins that tag
	httpClient     *http.Client
	log            *slog.Logger
	mu             sync.RWMutex // guards the binary on disk and pinnedVersion during updates
}

// New creates a Manager that stores the yt-dlp binary inside dataDir.
// No I/O is performed until EnsureReady is called.
func New(dataDir string, log *slog.Logger) *Manager {
	return &Manager{
		binPath:        filepath.Join(dataDir, "yt-dlp"),
		releaseBaseURL: githubReleasesBaseURL,
		httpClient: &http.Client{
			// ResponseHeaderTimeout prevents hung connections while still
			// allowing large binary downloads to stream without a deadline.
			Transport: &http.Transport{
				ResponseHeaderTimeout: 30 * time.Second,
			},
		},
		log: log,
	}
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

		_, dlURL, ferr := fetchRelease(ctx, m.httpClient, m.releaseBaseURL, "")
		if ferr != nil {
			return fmt.Errorf("fetch yt-dlp release: %w", ferr)
		}

		if ferr := downloadBinary(ctx, m.httpClient, dlURL, m.binPath); ferr != nil {
			return fmt.Errorf("install yt-dlp: %w", ferr)
		}

	case err != nil:
		return fmt.Errorf("stat yt-dlp binary: %w", err)
	}

	if err := m.smokeTest(ctx); err != nil {
		return err
	}

	go m.checkForUpdate(ctx)

	return nil
}

// SetVersion installs a specific yt-dlp version and pins the Manager to it.
// Pass "latest" or "" to switch back to tracking the latest release.
// Blocks until the version is installed and smoke-tested.
func (m *Manager) SetVersion(ctx context.Context, tag string) error {
	_, dlURL, err := fetchRelease(ctx, m.httpClient, m.releaseBaseURL, tag)
	if err != nil {
		return fmt.Errorf("fetch yt-dlp release %q: %w", tag, err)
	}

	backup := m.binPath + ".bak"

	m.mu.Lock()
	defer m.mu.Unlock()

	if err := os.Rename(m.binPath, backup); err != nil {
		return fmt.Errorf("back up yt-dlp before version switch: %w", err)
	}

	if err := downloadBinary(ctx, m.httpClient, dlURL, m.binPath); err != nil {
		m.recoverBinary(ctx, backup, dlURL)
		return fmt.Errorf("install yt-dlp %q: %w", tag, err)
	}

	if err := m.smokeTest(ctx); err != nil {
		m.recoverBinary(ctx, backup, dlURL)
		return fmt.Errorf("smoke test yt-dlp %q: %w", tag, err)
	}

	_ = os.Remove(backup)
	m.pinnedVersion = tag
	m.log.Info("yt-dlp version set", "version", tag)

	return nil
}

// Download invokes yt-dlp for a single video URL, writing all output lines to
// progress. The caller is responsible for constructing outDir (typically
// config.DownloadsDir/username). Context cancellation aborts the subprocess.
func (m *Manager) Download(ctx context.Context, url, outDir string, opts DownloadOpts, progress io.Writer) error {
	format, ok := qualityFormats[opts.Quality]
	if !ok {
		return fmt.Errorf("unknown quality %q", opts.Quality)
	}

	args := []string{
		"--newline",
		"--no-playlist",
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

	args = append(args, url)

	// Hold the read lock through Start so that a concurrent update cannot
	// rename the binary between when we capture the path and when the kernel
	// opens the file. Once Start returns the kernel holds the inode open and
	// a subsequent rename is safe.
	m.mu.RLock()
	cmd := exec.CommandContext(ctx, m.binPath, args...)
	cmd.Stdout = progress
	cmd.Stderr = progress
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

	tag, dlURL, err := fetchRelease(ctx, m.httpClient, m.releaseBaseURL, version)
	if err != nil {
		m.log.Warn("yt-dlp update check failed", "err", err)
		return
	}

	out, err := exec.CommandContext(ctx, m.binPath, "--version").Output()
	if err != nil {
		m.log.Warn("yt-dlp update check: could not read current version", "err", err)
		return
	}

	current := strings.TrimSpace(string(out))
	if current == tag {
		m.log.Debug("yt-dlp is up to date", "version", tag)
		return
	}

	m.log.Info("updating yt-dlp", "current", current, "latest", tag)

	backup := m.binPath + ".bak"

	m.mu.Lock()
	defer m.mu.Unlock()

	if err := os.Rename(m.binPath, backup); err != nil {
		m.log.Warn("yt-dlp update: could not back up binary, skipping update", "err", err)
		return
	}

	if err := downloadBinary(ctx, m.httpClient, dlURL, m.binPath); err != nil {
		m.log.Warn("yt-dlp update: download failed, rolling back", "err", err)
		m.recoverBinary(ctx, backup, dlURL)

		return
	}

	if err := m.smokeTest(ctx); err != nil {
		m.log.Warn("yt-dlp update: smoke test failed, rolling back", "err", err)
		m.recoverBinary(ctx, backup, dlURL)

		return
	}

	_ = os.Remove(backup)
	m.log.Info("yt-dlp updated", "version", tag)
}

// recoverBinary is called when an update fails after the old binary has been
// moved to backup. It first attempts to restore the backup via rename; if that
// fails it re-downloads dlURL as a last resort. Failure at every step is logged
// at Error — the binary will be unavailable until EnsureReady runs again on
// the next application restart.
func (m *Manager) recoverBinary(ctx context.Context, backup, dlURL string) {
	rerr := os.Rename(backup, m.binPath)
	if rerr == nil {
		m.log.Info("yt-dlp rolled back to previous version")
		return
	}

	m.log.Error("yt-dlp rollback via rename failed, attempting re-download", "err", rerr)

	if err := downloadBinary(ctx, m.httpClient, dlURL, m.binPath); err != nil {
		m.log.Error(
			"yt-dlp recovery failed: binary is unavailable; restart the application to retry",
			"err", err,
		)

		return
	}

	if err := m.smokeTest(ctx); err != nil {
		m.log.Error(
			"yt-dlp recovery: re-downloaded binary failed smoke test; binary is unavailable; restart to retry",
			"err", err,
		)

		return
	}

	_ = os.Remove(backup)
	m.log.Info("yt-dlp recovered via re-download")
}
