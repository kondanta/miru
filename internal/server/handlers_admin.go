package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/kondanta/miru/internal/auth"
)

type adminCreateUserRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	IsAdmin  bool   `json:"is_admin"`
}

func (s *Server) handleAdminListUsers(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.QueryContext(r.Context(),
		`SELECT id, username, is_admin, quality, sponsorblock, grace_hours, created_at
		 FROM users ORDER BY created_at ASC`,
	)
	if err != nil {
		s.log.Error("admin list users", "err", err)
		writeJSON(w, http.StatusInternalServerError, errBody("internal server error"))
		return
	}
	defer func() { _ = rows.Close() }()

	users := make([]userResponse, 0)
	for rows.Next() {
		var u userResponse
		if err := scanUser(rows, &u); err != nil {
			s.log.Error("admin list users scan", "err", err)
			writeJSON(w, http.StatusInternalServerError, errBody("internal server error"))
			return
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		s.log.Error("admin list users rows", "err", err)
		writeJSON(w, http.StatusInternalServerError, errBody("internal server error"))
		return
	}

	writeJSON(w, http.StatusOK, users)
}

func (s *Server) handleAdminCreateUser(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var req adminCreateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid request body"))
		return
	}
	if req.Username == "" || req.Password == "" {
		writeJSON(w, http.StatusBadRequest, errBody("username and password are required"))
		return
	}
	if len(req.Password) < 8 || len(req.Password) > 72 {
		writeJSON(w, http.StatusBadRequest, errBody("password must be between 8 and 72 bytes"))
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		s.log.Error("admin create user: hash password", "err", err)
		writeJSON(w, http.StatusInternalServerError, errBody("internal server error"))
		return
	}

	id := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339)
	isAdmin := 0
	if req.IsAdmin {
		isAdmin = 1
	}
	if _, err := s.db.ExecContext(r.Context(),
		`INSERT INTO users (id, username, password, is_admin, created_at)
		 VALUES (?, ?, ?, ?, ?)`,
		id, req.Username, hash, isAdmin, now,
	); err != nil {
		// SQLite unique constraint on username returns a specific error string.
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			writeJSON(w, http.StatusConflict, errBody("username already exists"))
			return
		}
		s.log.Error("admin create user", "err", err)
		writeJSON(w, http.StatusInternalServerError, errBody("internal server error"))
		return
	}

	var u userResponse
	if err := scanUser(s.db.QueryRowContext(r.Context(),
		`SELECT id, username, is_admin, quality, sponsorblock, grace_hours, created_at
		 FROM users WHERE id = ?`, id,
	), &u); err != nil {
		s.log.Error("admin create user: read back", "err", err)
		writeJSON(w, http.StatusInternalServerError, errBody("internal server error"))
		return
	}
	writeJSON(w, http.StatusCreated, u)
}

func (s *Server) handleAdminDeleteUser(w http.ResponseWriter, r *http.Request) {
	claims, _ := r.Context().Value(keyClaims).(*auth.Claims)
	id := chi.URLParam(r, "id")

	if id == claims.Subject {
		writeJSON(w, http.StatusBadRequest, errBody("cannot delete your own account"))
		return
	}

	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		s.log.Error("admin delete user: begin tx", "err", err)
		writeJSON(w, http.StatusInternalServerError, errBody("internal server error"))
		return
	}
	defer func() { _ = tx.Rollback() }()

	// Remove downloads first to satisfy the FK constraint.
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM downloads WHERE user_id = ?`, id); err != nil {
		s.log.Error("admin delete user: delete downloads", "err", err)
		writeJSON(w, http.StatusInternalServerError, errBody("internal server error"))
		return
	}

	result, err := tx.ExecContext(r.Context(), `DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		s.log.Error("admin delete user", "err", err)
		writeJSON(w, http.StatusInternalServerError, errBody("internal server error"))
		return
	}
	if n, _ := result.RowsAffected(); n == 0 {
		writeJSON(w, http.StatusNotFound, errBody("user not found"))
		return
	}

	if err := tx.Commit(); err != nil {
		s.log.Error("admin delete user: commit", "err", err)
		writeJSON(w, http.StatusInternalServerError, errBody("internal server error"))
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
