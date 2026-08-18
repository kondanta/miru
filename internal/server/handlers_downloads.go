package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/kondanta/miru/internal/auth"
	"github.com/kondanta/miru/internal/downloader"
	"github.com/kondanta/miru/internal/queue"
	"github.com/kondanta/miru/internal/youtube"
)

const validQualitiesMsg = "invalid quality; accepted: best, 360p, 480p, 720p, 1080p, 1440p, 2160p, 4320p"

// downloadRecord is the API representation of a downloads row.
type downloadRecord struct {
	ID           string `json:"id"`
	YoutubeID    string `json:"youtube_id"`
	Title        string `json:"title"`
	Status       string `json:"status"`
	Quality      string `json:"quality"`
	SponsorBlock bool   `json:"sponsorblock"`
	Source       string `json:"source"`
	FilePath     string `json:"file_path,omitempty"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

func (s *Server) handleListDownloads(w http.ResponseWriter, r *http.Request) {
	claims, _ := r.Context().Value(keyClaims).(*auth.Claims)

	rows, err := s.db.QueryContext(r.Context(), `
		SELECT id, youtube_id, title, status, quality, sponsorblock, source,
		       COALESCE(file_path, ''), created_at, updated_at
		FROM downloads
		WHERE user_id = ?
		ORDER BY created_at DESC
		LIMIT 100`,
		claims.Subject,
	)
	if err != nil {
		s.log.Error("list downloads query", "err", err)
		writeJSON(w, http.StatusInternalServerError, errBody("internal server error"))
		return
	}
	defer func() { _ = rows.Close() }()

	downloads := make([]downloadRecord, 0)
	for rows.Next() {
		var d downloadRecord
		var sponsorblock int
		if err := rows.Scan(
			&d.ID, &d.YoutubeID, &d.Title, &d.Status, &d.Quality,
			&sponsorblock, &d.Source, &d.FilePath, &d.CreatedAt, &d.UpdatedAt,
		); err != nil {
			s.log.Error("scan download row", "err", err)
			writeJSON(w, http.StatusInternalServerError, errBody("internal server error"))
			return
		}
		d.SponsorBlock = sponsorblock == 1
		downloads = append(downloads, d)
	}
	if err := rows.Err(); err != nil {
		s.log.Error("downloads rows error", "err", err)
		writeJSON(w, http.StatusInternalServerError, errBody("internal server error"))
		return
	}

	writeJSON(w, http.StatusOK, downloads)
}

type createDownloadRequest struct {
	URL     string `json:"url"`
	Quality string `json:"quality"`
}

func (s *Server) handleCreateDownload(w http.ResponseWriter, r *http.Request) {
	claims, _ := r.Context().Value(keyClaims).(*auth.Claims)

	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var req createDownloadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid request body"))
		return
	}
	if req.URL == "" {
		writeJSON(w, http.StatusBadRequest, errBody("url is required"))
		return
	}
	if req.Quality != "" {
		if _, ok := downloader.QualityFormats[req.Quality]; !ok {
			writeJSON(w, http.StatusBadRequest, errBody(validQualitiesMsg))
			return
		}
	}

	youtubeID, err := youtube.ExtractVideoID(req.URL)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid YouTube URL"))
		return
	}

	// Fetch user preferences; quality from request overrides the default.
	var userQuality string
	var userSponsorBlock int
	if err := s.db.QueryRowContext(r.Context(),
		`SELECT quality, sponsorblock FROM users WHERE id = ?`, claims.Subject,
	).Scan(&userQuality, &userSponsorBlock); err != nil {
		s.log.Error("fetch user prefs", "err", err)
		writeJSON(w, http.StatusInternalServerError, errBody("internal server error"))
		return
	}
	quality := req.Quality
	if quality == "" {
		quality = userQuality
	}

	id := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := s.db.ExecContext(r.Context(), `
		INSERT INTO downloads
		  (id, user_id, youtube_id, title, status, quality, sponsorblock, source, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'queued', ?, ?, 'manual', ?, ?)`,
		id, claims.Subject, youtubeID, req.URL, quality, userSponsorBlock, now, now,
	); err != nil {
		s.log.Error("insert download", "err", err)
		writeJSON(w, http.StatusInternalServerError, errBody("internal server error"))
		return
	}

	if !s.queue.Enqueue(queue.Job{
		ID:           id,
		UserID:       claims.Subject,
		YoutubeID:    youtubeID,
		URL:          req.URL,
		Quality:      quality,
		SponsorBlock: userSponsorBlock == 1,
	}) {
		// Enqueue rejected — server is shutting down. Mark the row failed so the
		// client can see what happened; use a fresh context since request ctx may
		// already be done.
		failCtx, failCancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, _ = s.db.ExecContext(failCtx,
			`UPDATE downloads SET status='failed', updated_at=? WHERE id=?`,
			time.Now().UTC().Format(time.RFC3339), id)
		failCancel()
		writeJSON(w, http.StatusServiceUnavailable, errBody("server is shutting down, try again"))
		return
	}

	writeJSON(w, http.StatusAccepted, downloadRecord{
		ID:           id,
		YoutubeID:    youtubeID,
		Title:        req.URL,
		Status:       "queued",
		Quality:      quality,
		SponsorBlock: userSponsorBlock == 1,
		Source:       "manual",
		CreatedAt:    now,
		UpdatedAt:    now,
	})
}

func (s *Server) handleGetDownload(w http.ResponseWriter, r *http.Request) {
	claims, _ := r.Context().Value(keyClaims).(*auth.Claims)
	id := chi.URLParam(r, "id")

	var d downloadRecord
	var sponsorblock int
	err := s.db.QueryRowContext(r.Context(), `
		SELECT id, youtube_id, title, status, quality, sponsorblock, source,
		       COALESCE(file_path, ''), created_at, updated_at
		FROM downloads
		WHERE id = ? AND user_id = ?`,
		id, claims.Subject,
	).Scan(
		&d.ID, &d.YoutubeID, &d.Title, &d.Status, &d.Quality,
		&sponsorblock, &d.Source, &d.FilePath, &d.CreatedAt, &d.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, errBody("download not found"))
		return
	}
	if err != nil {
		s.log.Error("get download", "err", err)
		writeJSON(w, http.StatusInternalServerError, errBody("internal server error"))
		return
	}
	d.SponsorBlock = sponsorblock == 1

	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleDeleteDownload(w http.ResponseWriter, r *http.Request) {
	claims, _ := r.Context().Value(keyClaims).(*auth.Claims)
	id := chi.URLParam(r, "id")

	// Fetch file_path before marking deleted so we can clean up disk.
	var filePath sql.NullString
	err := s.db.QueryRowContext(r.Context(),
		`SELECT file_path FROM downloads WHERE id = ? AND user_id = ? AND status != 'deleted'`,
		id, claims.Subject,
	).Scan(&filePath)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, errBody("download not found"))
		return
	}
	if err != nil {
		s.log.Error("delete download lookup", "err", err)
		writeJSON(w, http.StatusInternalServerError, errBody("internal server error"))
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := s.db.ExecContext(r.Context(),
		`UPDATE downloads SET status='deleted', updated_at=? WHERE id=? AND user_id=?`, now, id, claims.Subject,
	); err != nil {
		s.log.Error("delete download update", "err", err)
		writeJSON(w, http.StatusInternalServerError, errBody("internal server error"))
		return
	}

	// Remove the per-download directory from disk, verifying it is contained
	// within cfg.DownloadsDir to prevent path traversal if file_path is tampered.
	if filePath.Valid && filePath.String != "" {
		dir := filepath.Dir(filePath.String)
		rel, err := filepath.Rel(s.cfg.DownloadsDir, dir)
		if err != nil || strings.HasPrefix(rel, "..") {
			s.log.Warn("delete download: file_path outside downloads dir, skipping removal",
				"download_id", id, "path", dir)
		} else if err := os.RemoveAll(dir); err != nil {
			s.log.Warn("delete download: remove files", "download_id", id, "err", err)
		}
	}

	w.WriteHeader(http.StatusNoContent)
}
