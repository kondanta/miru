// Package config handles loading and validating miru configuration from
// TOML files and environment variables.
package config

import (
	"context"
	"errors"
	"fmt"
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
	DataDir      string    `toml:"data_dir"`
	DownloadsDir string    `toml:"downloads_dir"`
	Port         int       `toml:"port"`
	LogLevel     string    `toml:"log_level"`
	OIDC         *OIDC     `toml:"oidc"`
	Google       *Google   `toml:"google"`
	Jellyfin     *Jellyfin `toml:"jellyfin"`
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

	applyOIDCEnv(cfg)
	applyGoogleEnv(cfg)
	applyJellyfinEnv(cfg)

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
	if cfg.Port == 0 {
		cfg.Port = DefaultPort
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = DefaultLogLevel
	}
}

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
	if !validLogLevels[cfg.LogLevel] {
		errs = append(errs, fmt.Sprintf("log_level %q must be one of: debug, info, warn, error", cfg.LogLevel))
	}

	errs = append(errs, validateOIDC(cfg.OIDC)...)
	errs = append(errs, validateGoogle(cfg.Google)...)
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

func validateGoogle(g *Google) []string {
	if g == nil {
		return nil
	}
	if g.ClientID == "" || g.ClientSecret == "" {
		return []string{"google: client_id and client_secret must both be set"}
	}
	return nil
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
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
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
