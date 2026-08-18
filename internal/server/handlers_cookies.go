package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"

	"github.com/kondanta/miru/internal/downloader"
)

type putCookiesRequest struct {
	CookieHeader string `json:"cookie_header"`
}

// handleAdminPutCookies accepts a raw Cookie header value, writes it as a
// Netscape-format file in the data directory, and hot-swaps it into the
// downloader without a restart.
func (s *Server) handleAdminPutCookies(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<16) // 64 KB — plenty for any cookie header
	var req putCookiesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid request body"))
		return
	}
	if req.CookieHeader == "" {
		writeJSON(w, http.StatusBadRequest, errBody("cookie_header is required"))
		return
	}

	outPath := filepath.Join(s.cfg.DataDir, "cookies.txt")
	if err := downloader.WriteNetscapeCookies(req.CookieHeader, ".youtube.com", outPath); err != nil {
		s.log.Error("write cookies file", "err", err)
		writeJSON(w, http.StatusInternalServerError, errBody("failed to write cookies file"))
		return
	}

	s.dl.SetCookiesFile(outPath)
	s.log.Info("cookies updated", "path", outPath)

	writeJSON(w, http.StatusOK, map[string]string{"path": outPath})
}

// handleAdminDeleteCookies removes the cookies file and disables cookie-based
// auth for subsequent downloads.
func (s *Server) handleAdminDeleteCookies(w http.ResponseWriter, r *http.Request) {
	outPath := filepath.Join(s.cfg.DataDir, "cookies.txt")
	if err := os.Remove(outPath); err != nil && !os.IsNotExist(err) {
		s.log.Error("remove cookies file", "err", err)
		writeJSON(w, http.StatusInternalServerError, errBody("failed to remove cookies file"))
		return
	}

	s.dl.SetCookiesFile("")
	s.log.Info("cookies removed")

	w.WriteHeader(http.StatusNoContent)
}
