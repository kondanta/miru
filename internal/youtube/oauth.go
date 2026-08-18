package youtube

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// Scope is the OAuth scope required to read and delete YouTube playlist items.
// https://www.googleapis.com/auth/youtube covers playlistItems.list and
// playlistItems.delete — the minimum needed for Watch Later polling.
const Scope = "https://www.googleapis.com/auth/youtube"

// OAuthConfig builds a golang.org/x/oauth2 Config for the Google YouTube scope.
// redirectURL must be the fully-qualified callback URL (e.g. baseURL + "/api/v1/watch-later/auth/callback").
func OAuthConfig(clientID, clientSecret, redirectURL string) *oauth2.Config {
	// Use the v2 auth endpoint — the legacy v1 (/o/oauth2/auth) returns
	// "flowName: undefined" errors with certain scope combinations.
	ep := google.Endpoint
	ep.AuthURL = "https://accounts.google.com/o/oauth2/v2/auth"
	return &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
		Scopes:       []string{Scope},
		Endpoint:     ep,
	}
}

// Token holds the OAuth2 token fields we persist.
type Token struct {
	AccessToken  string
	RefreshToken string
	Expiry       time.Time
}

// toOAuth2 converts to an oauth2.Token so the oauth2 library can refresh it.
func (t Token) toOAuth2() *oauth2.Token {
	return &oauth2.Token{
		AccessToken:  t.AccessToken,
		RefreshToken: t.RefreshToken,
		Expiry:       t.Expiry,
	}
}

// TokenClient returns an http.Client that automatically refreshes the token
// using the stored refresh token.
func TokenClient(ctx context.Context, cfg *oauth2.Config, t Token) *http.Client {
	return cfg.Client(ctx, t.toOAuth2())
}

// encrypt encrypts plaintext with AES-256-GCM using key (must be 32 bytes).
// Returns base64(nonce || ciphertext).
func encrypt(key []byte, plaintext string) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("aes gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("nonce: %w", err)
	}
	ct := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ct), nil
}

// decrypt reverses encrypt. Returns ErrKeyMismatch if the tag check fails,
// which signals to callers (handlers) that the user must reconnect.
var ErrKeyMismatch = errors.New("token decryption failed: key mismatch or corrupt data")

func decrypt(key []byte, b64 string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", fmt.Errorf("base64: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("aes gcm: %w", err)
	}
	ns := gcm.NonceSize()
	if len(data) < ns {
		return "", ErrKeyMismatch
	}
	pt, err := gcm.Open(nil, data[:ns], data[ns:], nil)
	if err != nil {
		return "", ErrKeyMismatch
	}
	return string(pt), nil
}

// SaveToken encrypts and persists the OAuth token for userID into youtube_tokens.
func SaveToken(ctx context.Context, db *sql.DB, encKey []byte, userID string, t *oauth2.Token) error {
	encAccess, err := encrypt(encKey, t.AccessToken)
	if err != nil {
		return fmt.Errorf("encrypt access_token: %w", err)
	}
	encRefresh, err := encrypt(encKey, t.RefreshToken)
	if err != nil {
		return fmt.Errorf("encrypt refresh_token: %w", err)
	}
	expiry := t.Expiry.UTC().Format(time.RFC3339)
	_, err = db.ExecContext(ctx, `
		INSERT INTO youtube_tokens (user_id, access_token, refresh_token, expiry)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			access_token  = excluded.access_token,
			refresh_token = excluded.refresh_token,
			expiry        = excluded.expiry`,
		userID, encAccess, encRefresh, expiry,
	)
	return err
}

// LoadToken reads and decrypts the stored token for userID.
// Returns sql.ErrNoRows when no token exists.
// Returns ErrKeyMismatch when decryption fails (key was rotated).
func LoadToken(ctx context.Context, db *sql.DB, encKey []byte, userID string) (Token, error) {
	var encAccess, encRefresh, expiryStr string
	err := db.QueryRowContext(ctx,
		`SELECT access_token, refresh_token, expiry FROM youtube_tokens WHERE user_id = ?`, userID,
	).Scan(&encAccess, &encRefresh, &expiryStr)
	if err != nil {
		return Token{}, err
	}
	access, err := decrypt(encKey, encAccess)
	if err != nil {
		return Token{}, err
	}
	refresh, err := decrypt(encKey, encRefresh)
	if err != nil {
		return Token{}, err
	}
	expiry, err := time.Parse(time.RFC3339, expiryStr)
	if err != nil {
		return Token{}, fmt.Errorf("parse expiry: %w", err)
	}
	return Token{AccessToken: access, RefreshToken: refresh, Expiry: expiry}, nil
}

// DeleteToken removes the stored token for userID and revokes it with Google.
// Revocation is best-effort: a revocation failure is logged but does not block
// the local delete.
func DeleteToken(ctx context.Context, db *sql.DB, encKey []byte, userID string) error {
	tok, err := LoadToken(ctx, db, encKey, userID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) && !errors.Is(err, ErrKeyMismatch) {
		return fmt.Errorf("load token for revocation: %w", err)
	}
	if err == nil {
		// Best-effort revocation — ignore errors (network may be down, token may
		// already be expired). The local delete below is what matters for security.
		_ = revokeToken(ctx, tok.RefreshToken)
	}
	_, err = db.ExecContext(ctx, `DELETE FROM youtube_tokens WHERE user_id = ?`, userID)
	return err
}

// revokeToken calls Google's token revocation endpoint.
func revokeToken(ctx context.Context, refreshToken string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://oauth2.googleapis.com/revoke",
		nil,
	)
	if err != nil {
		return err
	}
	q := url.Values{}
	q.Set("token", refreshToken)
	req.URL.RawQuery = q.Encode()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// EnsureWatchLaterConfig creates the watch_later_configs row for userID if it
// does not already exist, using the server default poll interval.
func EnsureWatchLaterConfig(ctx context.Context, db *sql.DB, userID string, defaultInterval int) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO watch_later_configs (user_id, enabled, poll_interval_minutes)
		VALUES (?, 1, ?)
		ON CONFLICT(user_id) DO UPDATE SET enabled = 1`,
		userID, defaultInterval,
	)
	return err
}
