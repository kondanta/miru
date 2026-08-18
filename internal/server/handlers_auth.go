package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/golang-jwt/jwt/v5"
	"github.com/kondanta/miru/internal/auth"
)

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	Token string `json:"token"`
}

func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid request body"))
		return
	}
	if req.Username == "" || req.Password == "" {
		writeJSON(w, http.StatusBadRequest, errBody("username and password are required"))
		return
	}

	var (
		userID       string
		passwordHash sql.NullString
		isAdmin      bool
		tokenVersion int
	)
	err := s.db.QueryRowContext(r.Context(),
		`SELECT id, password, is_admin, token_version FROM users WHERE username = ?`,
		req.Username,
	).Scan(&userID, &passwordHash, &isAdmin, &tokenVersion)
	if errors.Is(err, sql.ErrNoRows) {
		// Normalize timing to prevent username enumeration.
		_ = auth.CheckPassword(dummyHash, req.Password)
		writeJSON(w, http.StatusUnauthorized, errBody("invalid credentials"))
		return
	}
	if err != nil {
		s.log.Error("login query", "err", err)
		writeJSON(w, http.StatusInternalServerError, errBody("internal server error"))
		return
	}

	if !passwordHash.Valid || passwordHash.String == "" {
		// OIDC-only user has no password set — local login is not allowed.
		// Run dummyHash to normalize response time and prevent OIDC-account enumeration.
		_ = auth.CheckPassword(dummyHash, req.Password)
		writeJSON(w, http.StatusUnauthorized, errBody("invalid credentials"))
		return
	}

	if err := auth.CheckPassword(passwordHash.String, req.Password); err != nil {
		writeJSON(w, http.StatusUnauthorized, errBody("invalid credentials"))
		return
	}

	token, err := auth.SignToken(auth.Claims{
		Username:         req.Username,
		IsAdmin:          isAdmin,
		TokenVersion:     tokenVersion,
		RegisteredClaims: jwt.RegisteredClaims{Subject: userID},
	}, []byte(s.cfg.JWTSecret), jwtTTL)
	if err != nil {
		s.log.Error("sign token", "err", err)
		writeJSON(w, http.StatusInternalServerError, errBody("internal server error"))
		return
	}

	writeJSON(w, http.StatusOK, loginResponse{Token: token})
}

func (s *Server) handleOIDCLogin(w http.ResponseWriter, _ *http.Request)    { notImplemented(w) }
func (s *Server) handleOIDCCallback(w http.ResponseWriter, _ *http.Request) { notImplemented(w) }
