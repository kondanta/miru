package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/kondanta/miru/internal/downloader"
)

type userResponse struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	IsAdmin      bool   `json:"is_admin"`
	Quality      string `json:"quality"`
	SponsorBlock bool   `json:"sponsorblock"`
	GraceHours   int    `json:"grace_hours"`
	CreatedAt    string `json:"created_at"`
}

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

// scanUser scans id, username, is_admin, quality, sponsorblock, grace_hours, created_at
// from row into u, converting the SQLite sponsorblock integer to a bool.
func scanUser(row scanner, u *userResponse) error {
	var sponsorblock int
	if err := row.Scan(
		&u.ID, &u.Username, &u.IsAdmin, &u.Quality, &sponsorblock, &u.GraceHours, &u.CreatedAt,
	); err != nil {
		return err
	}
	u.SponsorBlock = sponsorblock == 1
	return nil
}

func (s *Server) handleGetUser(w http.ResponseWriter, r *http.Request) {
	claims, ok := claimsFrom(w, r)
	if !ok {
		return
	}

	var u userResponse
	err := scanUser(s.db.QueryRowContext(r.Context(),
		`SELECT id, username, is_admin, quality, sponsorblock, grace_hours, created_at
		 FROM users WHERE id = ?`, claims.Subject,
	), &u)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, errBody("user not found"))
		return
	}
	if err != nil {
		s.log.Error("get user", "err", err)
		writeJSON(w, http.StatusInternalServerError, errBody("internal server error"))
		return
	}

	writeJSON(w, http.StatusOK, u)
}

type patchUserRequest struct {
	Quality      *string `json:"quality"`
	SponsorBlock *bool   `json:"sponsorblock"`
	GraceHours   *int    `json:"grace_hours"`
}

func (s *Server) handlePatchUser(w http.ResponseWriter, r *http.Request) {
	claims, ok := claimsFrom(w, r)
	if !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var req patchUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid request body"))
		return
	}

	if req.Quality != nil {
		if _, ok := downloader.QualityFormats[*req.Quality]; !ok {
			writeJSON(w, http.StatusBadRequest, errBody(validQualitiesMsg))
			return
		}
	}
	if req.GraceHours != nil && *req.GraceHours < 0 {
		writeJSON(w, http.StatusBadRequest, errBody("grace_hours must be non-negative"))
		return
	}

	// Build a single UPDATE from whichever fields were provided.
	setClauses := make([]string, 0, 3)
	args := make([]any, 0, 4)
	if req.Quality != nil {
		setClauses = append(setClauses, "quality = ?")
		args = append(args, *req.Quality)
	}
	if req.SponsorBlock != nil {
		sb := 0
		if *req.SponsorBlock {
			sb = 1
		}
		setClauses = append(setClauses, "sponsorblock = ?")
		args = append(args, sb)
	}
	if req.GraceHours != nil {
		setClauses = append(setClauses, "grace_hours = ?")
		args = append(args, *req.GraceHours)
	}

	if len(setClauses) == 0 {
		// Nothing to update — return current state.
		s.handleGetUser(w, r)
		return
	}

	args = append(args, claims.Subject)
	query := "UPDATE users SET " + strings.Join(setClauses, ", ") + " WHERE id = ?"
	if _, err := s.db.ExecContext(r.Context(), query, args...); err != nil {
		s.log.Error("patch user", "err", err)
		writeJSON(w, http.StatusInternalServerError, errBody("internal server error"))
		return
	}

	// Return the updated user.
	s.handleGetUser(w, r)
}
