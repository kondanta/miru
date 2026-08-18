package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testSecret is a 32-character string used as a valid JWT secret in tests.
const testSecret = "test-secret-that-is-long-enough!!"

func writeConfig(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	return path
}

func TestDefaultConfigPath_XDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/custom/xdg")

	got, err := defaultConfigPath()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := "/custom/xdg/miru/config.toml"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDefaultConfigPath_Home(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	got, err := defaultConfigPath()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.HasSuffix(got, filepath.Join(".miru", "config.toml")) {
		t.Errorf("got %q, want suffix .miru/config.toml", got)
	}
}

func TestLoad_CancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Load(ctx, "")
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
}

func TestLoad_MissingRequiredFields(t *testing.T) {
	// No file, no env vars — required fields must be reported together.
	_, err := Load(context.Background(), filepath.Join(t.TempDir(), "nonexistent.toml"))
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}
	for _, field := range []string{"data_dir", "downloads_dir", "jwt_secret"} {
		if !strings.Contains(err.Error(), field) {
			t.Errorf("error %q does not mention %q", err, field)
		}
	}
}

func TestLoad_EnvOnly(t *testing.T) {
	t.Setenv("MIRU_DATA_DIR", t.TempDir())
	t.Setenv("MIRU_DOWNLOADS_DIR", t.TempDir())
	t.Setenv("MIRU_JWT_SECRET", testSecret)

	cfg, err := Load(context.Background(), filepath.Join(t.TempDir(), "nonexistent.toml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Port != DefaultPort {
		t.Errorf("port: got %d, want %d", cfg.Port, DefaultPort)
	}
	if cfg.LogLevel != DefaultLogLevel {
		t.Errorf("log_level: got %q, want %q", cfg.LogLevel, DefaultLogLevel)
	}
}

func TestLoad_FileOnly(t *testing.T) {
	path := writeConfig(t, `
data_dir = "/data"
downloads_dir = "/downloads"
port = 9090
log_level = "debug"
jwt_secret = "`+testSecret+`"
`)

	cfg, err := Load(context.Background(), path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DataDir != "/data" {
		t.Errorf("data_dir: got %q, want /data", cfg.DataDir)
	}
	if cfg.Port != 9090 {
		t.Errorf("port: got %d, want 9090", cfg.Port)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("log_level: got %q, want debug", cfg.LogLevel)
	}
}

func TestLoad_EnvOverridesFile(t *testing.T) {
	path := writeConfig(t, `
data_dir = "/data"
downloads_dir = "/downloads"
port = 9000
jwt_secret = "`+testSecret+`"
`)
	t.Setenv("MIRU_PORT", "7777")

	cfg, err := Load(context.Background(), path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Port != 7777 {
		t.Errorf("port: got %d, want 7777", cfg.Port)
	}
}

func TestLoad_InvalidPort(t *testing.T) {
	t.Setenv("MIRU_PORT", "not-a-number")

	_, err := Load(context.Background(), filepath.Join(t.TempDir(), "nonexistent.toml"))
	if err == nil {
		t.Fatal("expected error for invalid MIRU_PORT, got nil")
	}
	if !strings.Contains(err.Error(), "MIRU_PORT") {
		t.Errorf("error %q does not mention MIRU_PORT", err)
	}
}

func TestLoad_PortZero(t *testing.T) {
	// MIRU_PORT=0 must error, not silently become DefaultPort.
	t.Setenv("MIRU_PORT", "0")

	_, err := Load(context.Background(), filepath.Join(t.TempDir(), "nonexistent.toml"))
	if err == nil {
		t.Fatal("expected error for MIRU_PORT=0, got nil")
	}
	if !strings.Contains(err.Error(), "MIRU_PORT") {
		t.Errorf("error %q does not mention MIRU_PORT", err)
	}
}

func TestLoad_JWTSecretMissing(t *testing.T) {
	t.Setenv("MIRU_DATA_DIR", "/data")
	t.Setenv("MIRU_DOWNLOADS_DIR", "/downloads")

	_, err := Load(context.Background(), filepath.Join(t.TempDir(), "nonexistent.toml"))
	if err == nil {
		t.Fatal("expected error for missing jwt_secret, got nil")
	}
	if !strings.Contains(err.Error(), "jwt_secret") {
		t.Errorf("error %q does not mention jwt_secret", err)
	}
}

func TestLoad_JWTSecretTooShort(t *testing.T) {
	t.Setenv("MIRU_DATA_DIR", "/data")
	t.Setenv("MIRU_DOWNLOADS_DIR", "/downloads")
	t.Setenv("MIRU_JWT_SECRET", "tooshort")

	_, err := Load(context.Background(), filepath.Join(t.TempDir(), "nonexistent.toml"))
	if err == nil {
		t.Fatal("expected error for short jwt_secret, got nil")
	}
	if !strings.Contains(err.Error(), "jwt_secret") {
		t.Errorf("error %q does not mention jwt_secret", err)
	}
}

func TestLoad_OIDCPartial(t *testing.T) {
	path := writeConfig(t, `
data_dir = "/data"
downloads_dir = "/downloads"
[oidc]
issuer = "https://auth.example.com"
`)

	_, err := Load(context.Background(), path)
	if err == nil {
		t.Fatal("expected error for partial OIDC config, got nil")
	}
	if !strings.Contains(err.Error(), "oidc") {
		t.Errorf("error %q does not mention oidc", err)
	}
}

func TestLoad_OIDCFull(t *testing.T) {
	path := writeConfig(t, `
data_dir = "/data"
downloads_dir = "/downloads"
jwt_secret = "`+testSecret+`"
[oidc]
issuer = "https://auth.example.com"
client_id = "miru"
client_secret = "secret"
`)

	cfg, err := Load(context.Background(), path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.OIDC == nil || cfg.OIDC.Issuer != "https://auth.example.com" {
		t.Error("OIDC not loaded correctly")
	}
}

func TestLoad_GooglePartial(t *testing.T) {
	t.Setenv("MIRU_DATA_DIR", "/data")
	t.Setenv("MIRU_DOWNLOADS_DIR", "/downloads")
	t.Setenv("MIRU_GOOGLE_CLIENT_ID", "gid")
	// MIRU_GOOGLE_CLIENT_SECRET intentionally absent.

	_, err := Load(context.Background(), filepath.Join(t.TempDir(), "nonexistent.toml"))
	if err == nil {
		t.Fatal("expected error for partial Google config, got nil")
	}
	if !strings.Contains(err.Error(), "google") {
		t.Errorf("error %q does not mention google", err)
	}
}

func TestLoad_JellyfinMissingAPIKey(t *testing.T) {
	path := writeConfig(t, `
data_dir = "/data"
downloads_dir = "/downloads"
[jellyfin]
url = "http://jellyfin.local:8096"
`)

	_, err := Load(context.Background(), path)
	if err == nil {
		t.Fatal("expected error for missing Jellyfin api_key, got nil")
	}
	if !strings.Contains(err.Error(), "api_key") {
		t.Errorf("error %q does not mention api_key", err)
	}
}

func TestLoad_JellyfinNoHost(t *testing.T) {
	// url.Parse("https:example.com") succeeds but has no host.
	path := writeConfig(t, `
data_dir = "/data"
downloads_dir = "/downloads"
[jellyfin]
url = "https:example.com"
api_key = "key"
`)

	_, err := Load(context.Background(), path)
	if err == nil {
		t.Fatal("expected error for host-less Jellyfin URL, got nil")
	}
	if !strings.Contains(err.Error(), "jellyfin.url") {
		t.Errorf("error %q does not mention jellyfin.url", err)
	}
}

func TestLoad_JellyfinInvalidURL(t *testing.T) {
	path := writeConfig(t, `
data_dir = "/data"
downloads_dir = "/downloads"
[jellyfin]
url = "ftp://jellyfin.example.com"
api_key = "key"
`)

	_, err := Load(context.Background(), path)
	if err == nil {
		t.Fatal("expected error for non-http Jellyfin URL, got nil")
	}
	if !strings.Contains(err.Error(), "jellyfin.url") {
		t.Errorf("error %q does not mention jellyfin.url", err)
	}
}

func TestLoad_NFODefaults(t *testing.T) {
	t.Setenv("MIRU_DATA_DIR", "/data")
	t.Setenv("MIRU_DOWNLOADS_DIR", "/downloads")
	t.Setenv("MIRU_JWT_SECRET", testSecret)

	cfg, err := Load(context.Background(), filepath.Join(t.TempDir(), "nonexistent.toml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *cfg.NFO.MaxTags != 10 {
		t.Errorf("MaxTags: got %d, want 10", *cfg.NFO.MaxTags)
	}
	if *cfg.NFO.MaxPosterBytes != 10*MB {
		t.Errorf("MaxPosterBytes: got %d, want %d", *cfg.NFO.MaxPosterBytes, 10*MB)
	}
}

func TestLoad_NFOMaxTagsEnv(t *testing.T) {
	t.Setenv("MIRU_DATA_DIR", "/data")
	t.Setenv("MIRU_DOWNLOADS_DIR", "/downloads")
	t.Setenv("MIRU_JWT_SECRET", testSecret)
	t.Setenv("MIRU_NFO_MAX_TAGS", "25")

	cfg, err := Load(context.Background(), filepath.Join(t.TempDir(), "nonexistent.toml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *cfg.NFO.MaxTags != 25 {
		t.Errorf("MaxTags: got %d, want 25", *cfg.NFO.MaxTags)
	}
}

func TestLoad_NFOMaxTagsZero(t *testing.T) {
	t.Setenv("MIRU_NFO_MAX_TAGS", "0")

	_, err := Load(context.Background(), filepath.Join(t.TempDir(), "nonexistent.toml"))
	if err == nil {
		t.Fatal("expected error for MIRU_NFO_MAX_TAGS=0, got nil")
	}
	if !strings.Contains(err.Error(), "MIRU_NFO_MAX_TAGS") {
		t.Errorf("error %q does not mention MIRU_NFO_MAX_TAGS", err)
	}
}

func TestLoad_NFOMaxPosterBytesEnv(t *testing.T) {
	t.Setenv("MIRU_DATA_DIR", "/data")
	t.Setenv("MIRU_DOWNLOADS_DIR", "/downloads")
	t.Setenv("MIRU_JWT_SECRET", testSecret)
	t.Setenv("MIRU_NFO_MAX_POSTER_BYTES", "20MB")

	cfg, err := Load(context.Background(), filepath.Join(t.TempDir(), "nonexistent.toml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *cfg.NFO.MaxPosterBytes != 20*MB {
		t.Errorf("MaxPosterBytes: got %d, want %d", *cfg.NFO.MaxPosterBytes, 20*MB)
	}
}

func TestLoad_NFOMaxPosterBytesFile(t *testing.T) {
	path := writeConfig(t, `
data_dir = "/data"
downloads_dir = "/downloads"
jwt_secret = "`+testSecret+`"
[nfo]
max_poster_bytes = "1GB"
`)

	cfg, err := Load(context.Background(), path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *cfg.NFO.MaxPosterBytes != GB {
		t.Errorf("MaxPosterBytes: got %d, want %d (1 GiB)", *cfg.NFO.MaxPosterBytes, GB)
	}
}

func TestLoad_NFOMaxPosterBytesFileInteger(t *testing.T) {
	// TOML integers (not strings) must also decode into ByteSize correctly.
	path := writeConfig(t, `
data_dir = "/data"
downloads_dir = "/downloads"
jwt_secret = "`+testSecret+`"
[nfo]
max_poster_bytes = 10485760
`)

	cfg, err := Load(context.Background(), path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *cfg.NFO.MaxPosterBytes != 10*MB {
		t.Errorf("MaxPosterBytes: got %d, want %d (10 MiB)", *cfg.NFO.MaxPosterBytes, 10*MB)
	}
}

func TestLoad_NFOMaxTagsTomlZero(t *testing.T) {
	path := writeConfig(t, `
data_dir = "/data"
downloads_dir = "/downloads"
jwt_secret = "`+testSecret+`"
[nfo]
max_tags = 0
`)

	_, err := Load(context.Background(), path)
	if err == nil {
		t.Fatal("expected error for TOML max_tags=0, got nil")
	}
	if !strings.Contains(err.Error(), "nfo.max_tags") {
		t.Errorf("error %q does not mention nfo.max_tags", err)
	}
}

func TestLoad_NFOMaxPosterBytesTomlZero(t *testing.T) {
	path := writeConfig(t, `
data_dir = "/data"
downloads_dir = "/downloads"
jwt_secret = "`+testSecret+`"
[nfo]
max_poster_bytes = 0
`)

	_, err := Load(context.Background(), path)
	if err == nil {
		t.Fatal("expected error for TOML max_poster_bytes=0, got nil")
	}
	if !strings.Contains(err.Error(), "nfo.max_poster_bytes") {
		t.Errorf("error %q does not mention nfo.max_poster_bytes", err)
	}
}

func TestLoad_NFOInvalidPosterBytes(t *testing.T) {
	t.Setenv("MIRU_NFO_MAX_POSTER_BYTES", "notabytes")

	_, err := Load(context.Background(), filepath.Join(t.TempDir(), "nonexistent.toml"))
	if err == nil {
		t.Fatal("expected error for invalid MIRU_NFO_MAX_POSTER_BYTES")
	}
	if !strings.Contains(err.Error(), "MIRU_NFO_MAX_POSTER_BYTES") {
		t.Errorf("error %q does not mention MIRU_NFO_MAX_POSTER_BYTES", err)
	}
}

func TestLoad_JellyfinValid(t *testing.T) {
	path := writeConfig(t, `
data_dir = "/data"
downloads_dir = "/downloads"
jwt_secret = "`+testSecret+`"
[jellyfin]
url = "http://jellyfin.local:8096"
api_key = "key"
`)

	cfg, err := Load(context.Background(), path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Jellyfin == nil || cfg.Jellyfin.URL != "http://jellyfin.local:8096" {
		t.Error("Jellyfin not loaded correctly")
	}
}
