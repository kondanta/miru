package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/kondanta/miru/internal/youtube"
	"golang.org/x/oauth2"
)

type watchLaterResponse struct {
	Enabled             bool    `json:"enabled"`
	PollIntervalMinutes int     `json:"poll_interval_minutes"`
	LastPolled          *string `json:"last_polled"`
	PlaylistID          *string `json:"playlist_id"`
}

func (s *Server) handleGetWatchLater(w http.ResponseWriter, r *http.Request) {
	claims, ok := claimsFrom(w, r)
	if !ok {
		return
	}

	var resp watchLaterResponse
	var lastPolled, playlistID sql.NullString
	var enabled int
	err := s.db.QueryRowContext(r.Context(), `
		SELECT enabled, poll_interval_minutes, last_polled, playlist_id
		FROM watch_later_configs WHERE user_id=?`, claims.Subject,
	).Scan(&enabled, &resp.PollIntervalMinutes, &lastPolled, &playlistID)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusOK, watchLaterResponse{
			Enabled:             false,
			PollIntervalMinutes: s.cfg.WatchLaterPollInterval,
		})
		return
	}
	if err != nil {
		s.log.Error("get watch later config", "err", err)
		writeJSON(w, http.StatusInternalServerError, errBody("internal server error"))
		return
	}
	resp.Enabled = enabled == 1
	if lastPolled.Valid && lastPolled.String != "" {
		resp.LastPolled = &lastPolled.String
	}
	if playlistID.Valid && playlistID.String != "" {
		resp.PlaylistID = &playlistID.String
	}
	writeJSON(w, http.StatusOK, resp)
}

type patchWatchLaterRequest struct {
	Enabled             *bool   `json:"enabled"`
	PollIntervalMinutes *int    `json:"poll_interval_minutes"`
	PlaylistID          *string `json:"playlist_id"`
}

func (s *Server) handlePutWatchLater(w http.ResponseWriter, r *http.Request) {
	claims, ok := claimsFrom(w, r)
	if !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var req patchWatchLaterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid request body"))
		return
	}
	if req.PollIntervalMinutes != nil && (*req.PollIntervalMinutes < 1 || *req.PollIntervalMinutes > maxPollInterval) {
		writeJSON(w, http.StatusBadRequest, errBody("poll_interval_minutes must be in [1, 4320]"))
		return
	}

	// Confirm the user has a Google token before allowing enable.
	if req.Enabled != nil && *req.Enabled {
		var count int
		_ = s.db.QueryRowContext(r.Context(),
			`SELECT COUNT(*) FROM youtube_tokens WHERE user_id=?`, claims.Subject,
		).Scan(&count)
		if count == 0 {
			writeJSON(w, http.StatusBadRequest, errBody("connect your Google account first via /watch-later/auth"))
			return
		}
	}

	if err := s.applyWatchLaterUpdate(r, claims.Subject, req); err != nil {
		s.log.Error("put watch later", "err", err)
		writeJSON(w, http.StatusInternalServerError, errBody("internal server error"))
		return
	}

	s.handleGetWatchLater(w, r)
}

func (s *Server) applyWatchLaterUpdate(r *http.Request, userID string, req patchWatchLaterRequest) error {
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(r.Context(), `
		INSERT OR IGNORE INTO watch_later_configs (user_id, poll_interval_minutes)
		VALUES (?, ?)`, userID, s.cfg.WatchLaterPollInterval,
	); err != nil {
		return err
	}

	if req.Enabled != nil {
		en := 0
		if *req.Enabled {
			en = 1
		}
		if _, err := tx.ExecContext(r.Context(),
			`UPDATE watch_later_configs SET enabled=? WHERE user_id=?`, en, userID,
		); err != nil {
			return err
		}
	}
	if req.PollIntervalMinutes != nil {
		if _, err := tx.ExecContext(r.Context(),
			`UPDATE watch_later_configs SET poll_interval_minutes=? WHERE user_id=?`,
			*req.PollIntervalMinutes, userID,
		); err != nil {
			return err
		}
	}
	if req.PlaylistID != nil {
		if _, err := tx.ExecContext(r.Context(),
			`UPDATE watch_later_configs SET playlist_id=? WHERE user_id=?`,
			*req.PlaylistID, userID,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

const maxPollInterval = 4320 // 72 hours in minutes

func (s *Server) handleWatchLaterAuth(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Google == nil {
		writeJSON(w, http.StatusNotImplemented, errBody("Google OAuth is not configured"))
		return
	}
	claims, ok := claimsFrom(w, r)
	if !ok {
		return
	}

	// state encodes the user ID so the callback can associate the token.
	// This is a simple personal deployment with no CSRF exposure, but using
	// the user ID as state is still correct: it binds the callback to the
	// initiating session.
	cfg := youtube.OAuthConfig(
		s.cfg.Google.ClientID,
		s.cfg.Google.ClientSecret,
		s.cfg.BaseURL+"/api/v1/watch-later/auth/callback",
	)
	// access_type=offline: request a refresh token (not just an access token).
	// prompt=consent: force Google to re-issue a refresh token even if the user
	// previously authorized the app — critical after a disconnect/revoke.
	authURL := cfg.AuthCodeURL(claims.Subject,
		oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("prompt", "consent"),
	)
	http.Redirect(w, r, authURL, http.StatusFound)
}

func (s *Server) handleWatchLaterAuthCallback(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Google == nil {
		writeJSON(w, http.StatusNotImplemented, errBody("Google OAuth is not configured"))
		return
	}

	userID := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	if userID == "" || code == "" {
		writeJSON(w, http.StatusBadRequest, errBody("missing state or code"))
		return
	}

	oauthCfg := youtube.OAuthConfig(
		s.cfg.Google.ClientID,
		s.cfg.Google.ClientSecret,
		s.cfg.BaseURL+"/api/v1/watch-later/auth/callback",
	)
	tok, err := oauthCfg.Exchange(r.Context(), code)
	if err != nil {
		s.log.Error("watch later auth callback: exchange", "err", err)
		writeJSON(w, http.StatusBadRequest, errBody("OAuth exchange failed"))
		return
	}

	if err := youtube.SaveToken(r.Context(), s.db, []byte(s.cfg.EncryptionKey), userID, tok); err != nil {
		s.log.Error("watch later auth callback: save token", "err", err)
		writeJSON(w, http.StatusInternalServerError, errBody("internal server error"))
		return
	}

	if err := youtube.EnsureWatchLaterConfig(r.Context(), s.db, userID, s.cfg.WatchLaterPollInterval); err != nil {
		s.log.Error("watch later auth callback: ensure config", "err", err)
		writeJSON(w, http.StatusInternalServerError, errBody("internal server error"))
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "connected"})
}

func (s *Server) handleDeleteWatchLater(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Google == nil {
		writeJSON(w, http.StatusNotImplemented, errBody("Google OAuth is not configured"))
		return
	}
	claims, ok := claimsFrom(w, r)
	if !ok {
		return
	}

	if err := youtube.DeleteToken(r.Context(), s.db, []byte(s.cfg.EncryptionKey), claims.Subject); err != nil {
		s.log.Error("delete watch later: revoke token", "err", err)
		writeJSON(w, http.StatusInternalServerError, errBody("internal server error"))
		return
	}

	// Disable the watch-later config row; leave it so interval prefs are preserved
	// if the user reconnects later.
	_, _ = s.db.ExecContext(r.Context(),
		`UPDATE watch_later_configs SET enabled=0 WHERE user_id=?`, claims.Subject,
	)

	w.WriteHeader(http.StatusNoContent)
}
