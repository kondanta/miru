package downloader

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeYtDlp writes a shell script to dataDir/yt-dlp that responds to
// --version with version and exits 0 for all other invocations.
func fakeYtDlp(t *testing.T, dataDir, version string) {
	t.Helper()

	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo \"" + version + "\"; fi\n"
	if err := os.WriteFile(filepath.Join(dataDir, "yt-dlp"), []byte(script), 0o755); err != nil {
		t.Fatalf("fakeYtDlp: %v", err)
	}
}

// releaseServer returns a test server that serves a mock GitHub releases API
// response and a /binary endpoint. Skips if the current platform is unsupported.
func releaseServer(t *testing.T, tag, binScript string) *httptest.Server {
	t.Helper()

	want, err := assetName(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skipf("platform not supported: %v", err)
	}

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/binary") {
			_, _ = w.Write([]byte(binScript))
			return
		}
		_ = json.NewEncoder(w).Encode(ghRelease{
			TagName: tag,
			Assets:  []ghAsset{{Name: want, BrowserDownloadURL: srv.URL + "/binary"}},
		})
	}))
	t.Cleanup(srv.Close)

	return srv
}

func newTestManager(t *testing.T, dataDir string, srv *httptest.Server) *Manager {
	t.Helper()

	m := New(dataDir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.releaseBaseURL = srv.URL

	return m
}

// --- release.go ---

func TestReleaseEndpoint(t *testing.T) {
	const base = "https://api.github.com/repos/yt-dlp/yt-dlp/releases"

	tests := []struct {
		tag  string
		want string
	}{
		{"", base + "/latest"},
		{"latest", base + "/latest"},
		{"2024.01.15", base + "/tags/2024.01.15"},
		{"2023.11.16", base + "/tags/2023.11.16"},
	}

	for _, tc := range tests {
		t.Run(tc.tag, func(t *testing.T) {
			got := releaseEndpoint(base, tc.tag)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAssetName(t *testing.T) {
	tests := []struct {
		goos, goarch string
		want         string
		wantErr      bool
	}{
		{"linux", "amd64", "yt-dlp", false},
		{"linux", "arm64", "yt-dlp_linux_aarch64", false},
		{"linux", "arm", "yt-dlp_linux_armv7l", false},
		{"windows", "amd64", "", true},
		{"darwin", "arm64", "", true},
	}

	for _, tc := range tests {
		t.Run(tc.goos+"/"+tc.goarch, func(t *testing.T) {
			got, err := assetName(tc.goos, tc.goarch)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}

				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFetchRelease(t *testing.T) {
	const tag = "2024.01.15"

	srv := releaseServer(t, tag, "#!/bin/sh\necho fake\n")

	gotTag, gotURL, err := fetchRelease(context.Background(), &http.Client{}, srv.URL, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotTag != tag {
		t.Errorf("tag: got %q, want %q", gotTag, tag)
	}
	if !strings.HasPrefix(gotURL, srv.URL) {
		t.Errorf("downloadURL %q does not point to test server", gotURL)
	}
}

func TestFetchRelease_ExplicitTag(t *testing.T) {
	const tag = "2023.11.16"

	srv := releaseServer(t, tag, "#!/bin/sh\necho fake\n")

	gotTag, _, err := fetchRelease(context.Background(), &http.Client{}, srv.URL, tag)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotTag != tag {
		t.Errorf("tag: got %q, want %q", gotTag, tag)
	}
}

func TestFetchRelease_MissingAsset(t *testing.T) {
	if _, err := assetName(runtime.GOOS, runtime.GOARCH); err != nil {
		t.Skipf("platform not supported: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(ghRelease{TagName: "2024.01.15", Assets: nil})
	}))
	t.Cleanup(srv.Close)

	_, _, err := fetchRelease(context.Background(), &http.Client{}, srv.URL, "")
	if err == nil {
		t.Fatal("expected error for release with no matching asset, got nil")
	}
}

func TestDownloadBinary(t *testing.T) {
	const content = "#!/bin/sh\necho fake-yt-dlp\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(content))
	}))
	t.Cleanup(srv.Close)

	dest := filepath.Join(t.TempDir(), "yt-dlp")

	if err := downloadBinary(context.Background(), &http.Client{}, srv.URL, dest); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("binary not found: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Error("binary is not executable")
	}

	got, _ := os.ReadFile(dest)
	if string(got) != content {
		t.Errorf("binary content: got %q, want %q", string(got), content)
	}
}

// --- manager.go ---

func TestEnsureReady_NoBinary(t *testing.T) {
	const version = "2024.01.15"

	dir := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo \"" + version + "\"; fi\n"
	srv := releaseServer(t, version, script)

	m := newTestManager(t, dir, srv)

	if err := m.EnsureReady(t.Context()); err != nil {
		t.Fatalf("EnsureReady: %v", err)
	}
	if _, err := os.Stat(m.binPath); err != nil {
		t.Errorf("binary not present after EnsureReady: %v", err)
	}
}

func TestEnsureReady_CachedBinary(t *testing.T) {
	const version = "2024.01.15"

	dir := t.TempDir()
	fakeYtDlp(t, dir, version)

	// Server returns the same version — update goroutine should be a no-op.
	srv := releaseServer(t, version, "")

	m := newTestManager(t, dir, srv)

	if err := m.EnsureReady(t.Context()); err != nil {
		t.Fatalf("EnsureReady with cached binary: %v", err)
	}
}

func TestDownload_UnknownQuality(t *testing.T) {
	m := New(t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))

	err := m.Download(context.Background(), "https://example.com", t.TempDir(), DownloadOpts{Quality: "8k"}, nil)
	if err == nil {
		t.Fatal("expected error for unknown quality, got nil")
	}
	if !strings.Contains(err.Error(), "unknown quality") {
		t.Errorf("error %q does not mention unknown quality", err)
	}
}

func TestDownload_InvalidScheme(t *testing.T) {
	m := New(t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))

	for _, rawURL := range []string{"ftp://example.com", "file:///etc/passwd", "-evil-flag", ""} {
		err := m.Download(t.Context(), rawURL, t.TempDir(), DownloadOpts{Quality: "720p"}, nil)
		if err == nil {
			t.Fatalf("expected error for URL %q, got nil", rawURL)
		}
	}
}

func TestDownload_Args(t *testing.T) {
	dir := t.TempDir()
	outDir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")

	// Fake yt-dlp records its args one-per-line and exits 0.
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\n"
	if err := os.WriteFile(filepath.Join(dir, "yt-dlp"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		opts       DownloadOpts
		wantArgs   []string
		absentArgs []string
	}{
		{
			name: "common flags always present",
			opts: DownloadOpts{Quality: "1080p"},
			wantArgs: []string{
				"--newline", "--no-playlist",
				"--merge-output-format", "mp4",
				"--output", "%(upload_date>%Y-%m-%d)s - %(title)s.%(ext)s",
			},
		},
		{
			name:     "1080p format",
			opts:     DownloadOpts{Quality: "1080p"},
			wantArgs: []string{"bestvideo[height<=1080]+bestaudio/best[height<=1080]"},
		},
		{
			name:     "2160p format",
			opts:     DownloadOpts{Quality: "2160p"},
			wantArgs: []string{"bestvideo[height<=2160]+bestaudio/best[height<=2160]"},
		},
		{
			name:     "4320p format",
			opts:     DownloadOpts{Quality: "4320p"},
			wantArgs: []string{"bestvideo[height<=4320]+bestaudio/best[height<=4320]"},
		},
		{
			name:     "sponsorblock enabled",
			opts:     DownloadOpts{Quality: "720p", SponsorBlock: true},
			wantArgs: []string{"--sponsorblock-remove", "all"},
		},
		{
			name:       "sponsorblock absent by default",
			opts:       DownloadOpts{Quality: "720p", SponsorBlock: false},
			absentArgs: []string{"--sponsorblock-remove"},
		},
		{
			name:     "write-info-json",
			opts:     DownloadOpts{Quality: "720p", WriteInfoJSON: true},
			wantArgs: []string{"--write-info-json"},
		},
		{
			name:       "write-info-json absent by default",
			opts:       DownloadOpts{Quality: "720p", WriteInfoJSON: false},
			absentArgs: []string{"--write-info-json"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Clear argsFile so stale args from a previous subtest cannot satisfy
			// this test's assertions.
			_ = os.Remove(argsFile)

			m := New(dir, slog.New(slog.NewTextHandler(io.Discard, nil)))
			err := m.Download(
				context.Background(), "https://example.com/watch?v=test", outDir, tc.opts, nil,
			)
			if err != nil {
				t.Fatalf("Download: %v", err)
			}

			raw, err := os.ReadFile(argsFile)
			if err != nil {
				t.Fatalf("args file not written (did the fake binary run?): %v", err)
			}
			argsStr := strings.TrimSpace(string(raw))

			for _, want := range tc.wantArgs {
				if !strings.Contains(argsStr, want) {
					t.Errorf("args missing %q\nfull args:\n%s", want, argsStr)
				}
			}
			for _, absent := range tc.absentArgs {
				if strings.Contains(argsStr, absent) {
					t.Errorf("args contains unexpected %q\nfull args:\n%s", absent, argsStr)
				}
			}
		})
	}
}
