// Package config handles loading and validating miru configuration from
// TOML files and environment variables.
package config

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

const (
	// DefaultPort is the default REST server port.
	DefaultPort = 8090

	// DefaultLogLevel is the default log level.
	DefaultLogLevel = "info"
)

type Config struct {
	DataDir              string    `toml:"data_dir"`
	DownloadsDir         string    `toml:"downloads_dir"`
	Port                 int       `toml:"port"`
	LogLevel             string    `toml:"log_level"`
	JWTSecret            string    `toml:"jwt_secret"`
	DownloadTimeoutHours int       `toml:"download_timeout_hours"`
	NFO                  NFOConfig `toml:"nfo"`
	OIDC                 *OIDC     `toml:"oidc"`
	Google               *Google   `toml:"google"`
	Jellyfin             *Jellyfin `toml:"jellyfin"`

	// BaseURL is the externally reachable URL miru is served on (e.g.
	// "https://miru.example.com"). Required when Google OAuth is configured —
	// used to construct the OAuth redirect URI.
	BaseURL string `toml:"base_url"`

	// EncryptionKey is a 32-byte (256-bit) key used to encrypt OAuth refresh
	// tokens at rest with AES-256-GCM. Required when Google OAuth is configured.
	// Env-only (MIRU_ENCRYPTION_KEY): no TOML tag so it cannot be written to
	// the config file on disk.
	// Hard rotation: changing this key makes existing tokens unreadable — affected
	// users must disconnect and reconnect their Google account. Versioned-key
	// rotation was considered but skipped: this is a personal single-deployment
	// tool; the same hard-rotation semantics already apply to JWTSecret.
	EncryptionKey string

	// WatchLaterPollInterval is the server-wide default interval (minutes)
	// between Watch Later polls. Per-user overrides are stored in the DB.
	// Range: [1, 4320] (1 minute to 72 hours). Default: 10.
	WatchLaterPollInterval int `toml:"watch_later_poll_interval"`

	// YtdlpCookiesFile is the path to a Netscape-format cookies file passed to
	// yt-dlp via --cookies. Optional — when set, helps bypass 403 errors on IPs
	// flagged by YouTube. Any logged-in YouTube browser session works; the file
	// is server-wide and applies to all users' downloads.
	// Env: MIRU_YTDLP_COOKIES_FILE.
	YtdlpCookiesFile string `toml:"ytdlp_cookies_file"`
}

// NFOConfig controls NFO metadata generation behaviour.
// Pointer fields distinguish "not set" (nil → default applies) from
// "explicitly set to zero" (non-nil → validation rejects it).
type NFOConfig struct {
	// MaxTags caps how many yt-dlp tags are written to the NFO file.
	MaxTags *int `toml:"max_tags"`
	// MaxPosterBytes is the maximum size of a downloaded poster image.
	// Accepts human-readable values like "10MB", "1GiB", or a plain integer (bytes).
	MaxPosterBytes *ByteSize `toml:"max_poster_bytes"`
}

// ByteSize is an int64 that can be unmarshalled from human-readable strings like
// "10MB", "1GiB", or a plain integer (interpreted as bytes). It is used in
// config fields that represent data sizes.
type ByteSize int64

const (
	_  = iota
	KB = ByteSize(1 << (10 * iota))
	MB
	GB
	TB
)

func (b *ByteSize) UnmarshalText(text []byte) error {
	n, err := parseByteSize(string(text))
	if err != nil {
		return err
	}
	*b = n
	return nil
}

// parseByteSize parses strings like "10MB", "1GiB", "500B", or "10485760".
// KB/MB/GB/TB and KiB/MiB/GiB/TiB are all treated as powers of 1024.
func parseByteSize(s string) (ByteSize, error) {
	s = strings.TrimSpace(s)
	upper := strings.ToUpper(s)

	units := []struct {
		suffix string
		size   ByteSize
	}{
		{"TIB", TB},
		{"GIB", GB},
		{"MIB", MB},
		{"KIB", KB},
		{"TB", TB},
		{"GB", GB},
		{"MB", MB},
		{"KB", KB},
		{"B", 1},
	}

	for _, u := range units {
		if strings.HasSuffix(upper, u.suffix) {
			numStr := strings.TrimSpace(s[:len(s)-len(u.suffix)])
			n, err := strconv.ParseInt(numStr, 10, 64)
			if err != nil || n < 0 {
				return 0, fmt.Errorf("invalid byte size %q", s)
			}
			// Guard against int64 overflow before multiplying.
			if u.size > 1 && n > math.MaxInt64/int64(u.size) {
				return 0, fmt.Errorf("invalid byte size %q: value too large", s)
			}
			return ByteSize(n) * u.size, nil
		}
	}

	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid byte size %q: use a number or a suffix like MB, GB", s)
	}
	return ByteSize(n), nil
}

type OIDC struct {
	Issuer       string `toml:"issuer"`
	ClientID     string `toml:"client_id"`
	ClientSecret string `toml:"client_secret"`
}

type Google struct {
	ClientID     string `toml:"client_id"`
	ClientSecret string `toml:"client_secret"`
}

type Jellyfin struct {
	URL    string `toml:"url"`
	APIKey string `toml:"api_key"`
}

// Load parses the config file at path, overlays environment variables, applies
// defaults, and validates the result. If path is empty the XDG default is used.
// If the config file does not exist, Load continues with an empty base so that
// a purely env-var driven setup (e.g. Docker) works without a file.
func Load(ctx context.Context, path string) (*Config, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if path == "" {
		p, err := defaultConfigPath()
		if err != nil {
			return nil, fmt.Errorf("resolving config path: %w", err)
		}
		path = p
	}

	cfg, err := parseFile(path)
	if err != nil {
		return nil, err
	}

	if err := applyEnv(cfg); err != nil {
		return nil, err
	}

	applyDefaults(cfg)

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return cfg, nil
}

func parseFile(path string) (*Config, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return &Config{}, nil
	}

	var cfg Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config file %q: %w", path, err)
	}

	return &cfg, nil
}

// applyEnv overlays MIRU_* environment variables onto cfg. File values are the
// base; env vars win. Any env var present for a section (OIDC, Google, Jellyfin)
// initialises that section so partial configuration is caught by validate.
func applyEnv(cfg *Config) error {
	if v := os.Getenv("MIRU_DATA_DIR"); v != "" {
		cfg.DataDir = v
	}
	if v := os.Getenv("MIRU_DOWNLOADS_DIR"); v != "" {
		cfg.DownloadsDir = v
	}
	if v := os.Getenv("MIRU_PORT"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil || p < 1 || p > 65535 {
			return fmt.Errorf("MIRU_PORT %q is not a valid port number", v)
		}
		cfg.Port = p
	}
	if v := os.Getenv("MIRU_LOG_LEVEL"); v != "" {
		cfg.LogLevel = v
	}
	if v := os.Getenv("MIRU_JWT_SECRET"); v != "" {
		cfg.JWTSecret = v
	}
	if v := os.Getenv("MIRU_DOWNLOAD_TIMEOUT_HOURS"); v != "" {
		h, err := strconv.Atoi(v)
		if err != nil || h < 1 || int64(h) > maxDownloadTimeoutHours {
			return fmt.Errorf(
				"MIRU_DOWNLOAD_TIMEOUT_HOURS %q: must be a positive integer <= %d",
				v,
				maxDownloadTimeoutHours,
			)
		}
		cfg.DownloadTimeoutHours = h
	}

	applyCookiesEnv(cfg)

	if err := applyWatchLaterEnv(cfg); err != nil {
		return err
	}
	if err := applyNFOEnv(cfg); err != nil {
		return err
	}
	applyOIDCEnv(cfg)
	applyGoogleEnv(cfg)
	applyJellyfinEnv(cfg)

	return nil
}

func applyCookiesEnv(cfg *Config) {
	if v := os.Getenv("MIRU_YTDLP_COOKIES_FILE"); v != "" {
		cfg.YtdlpCookiesFile = v
	}
}

func applyWatchLaterEnv(cfg *Config) error {
	if v := os.Getenv("MIRU_BASE_URL"); v != "" {
		cfg.BaseURL = v
	}
	if v := os.Getenv("MIRU_ENCRYPTION_KEY"); v != "" {
		cfg.EncryptionKey = v
	}
	if v := os.Getenv("MIRU_WATCH_LATER_POLL_INTERVAL"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxWatchLaterPollInterval {
			return fmt.Errorf(
				"MIRU_WATCH_LATER_POLL_INTERVAL %q: must be an integer in [1, %d]",
				v, maxWatchLaterPollInterval,
			)
		}
		cfg.WatchLaterPollInterval = n
	}
	return nil
}

func applyNFOEnv(cfg *Config) error {
	if v := os.Getenv("MIRU_NFO_MAX_TAGS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return fmt.Errorf("MIRU_NFO_MAX_TAGS %q: must be a positive integer", v)
		}
		cfg.NFO.MaxTags = &n
	}
	if v := os.Getenv("MIRU_NFO_MAX_POSTER_BYTES"); v != "" {
		n, err := parseByteSize(v)
		if err != nil {
			return fmt.Errorf("MIRU_NFO_MAX_POSTER_BYTES: %w", err)
		}
		if n < 1 {
			return fmt.Errorf("MIRU_NFO_MAX_POSTER_BYTES must be >= 1 byte")
		}
		cfg.NFO.MaxPosterBytes = &n
	}
	return nil
}

func applyOIDCEnv(cfg *Config) {
	issuer := os.Getenv("MIRU_OIDC_ISSUER")
	clientID := os.Getenv("MIRU_OIDC_CLIENT_ID")
	secret := os.Getenv("MIRU_OIDC_CLIENT_SECRET")

	if issuer == "" && clientID == "" && secret == "" {
		return
	}
	if cfg.OIDC == nil {
		cfg.OIDC = &OIDC{}
	}
	if issuer != "" {
		cfg.OIDC.Issuer = issuer
	}
	if clientID != "" {
		cfg.OIDC.ClientID = clientID
	}
	if secret != "" {
		cfg.OIDC.ClientSecret = secret
	}
}

func applyGoogleEnv(cfg *Config) {
	clientID := os.Getenv("MIRU_GOOGLE_CLIENT_ID")
	secret := os.Getenv("MIRU_GOOGLE_CLIENT_SECRET")

	if clientID == "" && secret == "" {
		return
	}
	if cfg.Google == nil {
		cfg.Google = &Google{}
	}
	if clientID != "" {
		cfg.Google.ClientID = clientID
	}
	if secret != "" {
		cfg.Google.ClientSecret = secret
	}
}

func applyJellyfinEnv(cfg *Config) {
	u := os.Getenv("MIRU_JELLYFIN_URL")
	key := os.Getenv("MIRU_JELLYFIN_API_KEY")

	if u == "" && key == "" {
		return
	}
	if cfg.Jellyfin == nil {
		cfg.Jellyfin = &Jellyfin{}
	}
	if u != "" {
		cfg.Jellyfin.URL = u
	}
	if key != "" {
		cfg.Jellyfin.APIKey = key
	}
}

func applyDefaults(cfg *Config) {
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.Port == 0 {
		cfg.Port = DefaultPort
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = DefaultLogLevel
	}
	if cfg.DownloadTimeoutHours == 0 {
		cfg.DownloadTimeoutHours = 6
	}
	if cfg.NFO.MaxTags == nil {
		v := 10
		cfg.NFO.MaxTags = &v
	}
	if cfg.NFO.MaxPosterBytes == nil {
		v := 10 * MB
		cfg.NFO.MaxPosterBytes = &v
	}
	if cfg.WatchLaterPollInterval == 0 {
		cfg.WatchLaterPollInterval = 10
	}
}

// maxDownloadTimeoutHours is math.MaxInt64 / int64(time.Hour) — the largest
// value that can be safely converted to time.Duration without overflow.
const maxDownloadTimeoutHours = math.MaxInt64 / int64(3_600_000_000_000)

// maxWatchLaterPollInterval is 72 hours expressed in minutes.
const maxWatchLaterPollInterval = 72 * 60

var validLogLevels = map[string]bool{
	"debug": true, "info": true, "warn": true, "error": true,
}

func (cfg *Config) validate() error {
	var errs []string

	if cfg.DataDir == "" {
		errs = append(errs, "data_dir is required (set via config or MIRU_DATA_DIR)")
	}
	if cfg.DownloadsDir == "" {
		errs = append(errs, "downloads_dir is required (set via config or MIRU_DOWNLOADS_DIR)")
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		errs = append(errs, fmt.Sprintf("port %d out of range [1, 65535]", cfg.Port))
	}
	if cfg.JWTSecret == "" {
		errs = append(errs, "jwt_secret is required (set via config or MIRU_JWT_SECRET)")
	} else if len(cfg.JWTSecret) < 32 {
		errs = append(errs, "jwt_secret must be at least 32 characters")
	}
	if !validLogLevels[cfg.LogLevel] {
		errs = append(errs, fmt.Sprintf("log_level %q must be one of: debug, info, warn, error", cfg.LogLevel))
	}

	if cfg.DownloadTimeoutHours < 1 || int64(cfg.DownloadTimeoutHours) > maxDownloadTimeoutHours {
		errs = append(errs, fmt.Sprintf(
			"download_timeout_hours %d out of range [1, %d]", cfg.DownloadTimeoutHours, maxDownloadTimeoutHours,
		))
	}
	if *cfg.NFO.MaxTags < 1 {
		errs = append(errs, "nfo.max_tags must be >= 1")
	}
	if *cfg.NFO.MaxPosterBytes < 1 {
		errs = append(errs, "nfo.max_poster_bytes must be >= 1 byte")
	}

	if cfg.WatchLaterPollInterval < 1 || cfg.WatchLaterPollInterval > maxWatchLaterPollInterval {
		errs = append(errs, fmt.Sprintf(
			"watch_later_poll_interval %d out of range [1, %d]",
			cfg.WatchLaterPollInterval, maxWatchLaterPollInterval,
		))
	}

	errs = append(errs, validateOIDC(cfg.OIDC)...)
	errs = append(errs, validateGoogle(cfg.Google, cfg.BaseURL, cfg.EncryptionKey)...)
	errs = append(errs, validateJellyfin(cfg.Jellyfin)...)

	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}

	return nil
}

func validateOIDC(o *OIDC) []string {
	if o == nil {
		return nil
	}
	if o.Issuer == "" || o.ClientID == "" || o.ClientSecret == "" {
		return []string{"oidc: issuer, client_id, and client_secret must all be set"}
	}
	return nil
}

func validateGoogle(g *Google, baseURL, encKey string) []string {
	if g == nil {
		return nil
	}
	var errs []string
	if g.ClientID == "" || g.ClientSecret == "" {
		errs = append(errs, "google: client_id and client_secret must both be set")
	}
	if baseURL == "" {
		errs = append(errs, "base_url is required when Google OAuth is configured (set MIRU_BASE_URL)")
	} else {
		u, err := url.Parse(baseURL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			errs = append(errs, fmt.Sprintf("base_url %q must be a valid http or https URL", baseURL))
		}
	}
	if len(encKey) != 32 {
		errs = append(errs, "MIRU_ENCRYPTION_KEY must be exactly 32 bytes when Google OAuth is configured")
	}
	return errs
}

func validateJellyfin(j *Jellyfin) []string {
	if j == nil {
		return nil
	}
	var errs []string
	if j.URL == "" {
		errs = append(errs, "jellyfin: url is required")
	} else {
		u, err := url.Parse(j.URL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			errs = append(errs, fmt.Sprintf("jellyfin.url %q must be a valid http or https URL", j.URL))
		}
	}
	if j.APIKey == "" {
		errs = append(errs, "jellyfin: api_key is required")
	}
	return errs
}

func defaultConfigPath() (string, error) {
	if base := os.Getenv("XDG_CONFIG_HOME"); base != "" {
		return filepath.Join(base, "miru", "config.toml"), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}

	return filepath.Join(home, ".miru", "config.toml"), nil
}
