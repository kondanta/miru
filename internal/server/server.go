// Package server wires the HTTP router, middleware, and handlers.
package server

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/kondanta/miru/internal/config"
	"github.com/kondanta/miru/internal/queue"
)

type contextKey int

const keyRequestID contextKey = 0

// Server holds shared dependencies for all HTTP handlers.
type Server struct {
	db    *sql.DB
	cfg   *config.Config
	queue *queue.Manager
	log   *slog.Logger
	web   fs.FS
}

// New creates a Server. web is the embedded SPA filesystem (may be nil to
// disable the catch-all SPA route during tests).
func New(db *sql.DB, cfg *config.Config, q *queue.Manager, log *slog.Logger, web fs.FS) *Server {
	return &Server{db: db, cfg: cfg, queue: q, log: log, web: web}
}

// Handler builds and returns the chi router. Call once at startup.
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()

	r.Use(s.recoverer)
	r.Use(s.requestID)
	r.Use(s.logger)

	r.Get("/healthz", s.handleHealthz)

	r.Route("/api/v1", func(r chi.Router) {
		// Unauthenticated auth endpoints.
		r.Post("/auth/login", s.handleAuthLogin)
		r.Get("/auth/oidc/login", s.handleOIDCLogin)
		r.Get("/auth/oidc/callback", s.handleOIDCCallback)

		// All routes below require a valid JWT.
		r.Group(func(r chi.Router) {
			r.Use(s.authenticate)

			r.Get("/user", s.handleGetUser)
			r.Patch("/user", s.handlePatchUser)

			// TODO: add cursor-based pagination to GET /downloads.
			r.Get("/downloads", s.handleListDownloads)
			r.Post("/downloads", s.handleCreateDownload)
			r.Get("/downloads/{id}", s.handleGetDownload)
			r.Delete("/downloads/{id}", s.handleDeleteDownload)

			r.Get("/watch-later", s.handleGetWatchLater)
			r.Put("/watch-later", s.handlePutWatchLater)
			r.Get("/watch-later/auth", s.handleWatchLaterAuth)
			r.Get("/watch-later/auth/callback", s.handleWatchLaterAuthCallback)

			r.Get("/webhooks", s.handleListWebhooks)
			r.Post("/webhooks", s.handleCreateWebhook)
			r.Delete("/webhooks/{id}", s.handleDeleteWebhook)

			r.Get("/jellyfin", s.handleGetJellyfin)
			r.Put("/jellyfin", s.handlePutJellyfin)

			// Admin-only routes.
			r.Group(func(r chi.Router) {
				r.Use(s.adminOnly)
				r.Get("/admin/users", s.handleAdminListUsers)
				r.Post("/admin/users", s.handleAdminCreateUser)
				r.Delete("/admin/users/{id}", s.handleAdminDeleteUser)
			})
		})
	})

	if s.web != nil {
		r.Handle("/*", s.spaHandler())
	}

	return r
}

// --- Middleware ---

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic recovered", "recover", rec, "path", r.URL.Path)
				if !rw.written {
					writeJSON(rw, http.StatusInternalServerError, errBody("internal server error"))
				}
			}
		}()
		next.ServeHTTP(rw, r)
	})
}

func (s *Server) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			id = newRequestID()
		}
		w.Header().Set("X-Request-ID", id)
		ctx := context.WithValue(r.Context(), keyRequestID, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// responseWriter wraps http.ResponseWriter to capture the written status code.
type responseWriter struct {
	http.ResponseWriter
	status  int
	written bool
}

func (rw *responseWriter) WriteHeader(code int) {
	if !rw.written {
		rw.status = code
		rw.written = true
		rw.ResponseWriter.WriteHeader(code)
	}
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	if !rw.written {
		rw.WriteHeader(http.StatusOK)
	}
	return rw.ResponseWriter.Write(b)
}

func (s *Server) logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		s.log.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.status,
			"duration", time.Since(start),
			"request_id", r.Context().Value(keyRequestID),
		)
	})
}

// authenticate verifies the JWT and injects the user into the request context.
// TODO: parse Bearer token, verify JWT, set user ID + is_admin in ctx (internal/auth not yet implemented).
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
	})
}

// adminOnly rejects non-admin requests.
// TODO: read is_admin from JWT claims set by authenticate; return 403 if false.
func (s *Server) adminOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
	})
}

// --- SPA ---

func (s *Server) spaHandler() http.Handler {
	fsrv := http.FileServer(http.FS(s.web))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Strip leading slash for fs.FS paths, which do not accept rooted paths.
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "."
		}
		if _, err := fs.Stat(s.web, name); errors.Is(err, fs.ErrNotExist) {
			// Unknown path — let the SPA router handle it.
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		fsrv.ServeHTTP(w, r)
	})
}

// --- Handlers ---

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Auth
func (s *Server) handleAuthLogin(w http.ResponseWriter, _ *http.Request)    { notImplemented(w) }
func (s *Server) handleOIDCLogin(w http.ResponseWriter, _ *http.Request)    { notImplemented(w) }
func (s *Server) handleOIDCCallback(w http.ResponseWriter, _ *http.Request) { notImplemented(w) }

// User
func (s *Server) handleGetUser(w http.ResponseWriter, _ *http.Request)   { notImplemented(w) }
func (s *Server) handlePatchUser(w http.ResponseWriter, _ *http.Request) { notImplemented(w) }

// Downloads
func (s *Server) handleListDownloads(w http.ResponseWriter, _ *http.Request)  { notImplemented(w) }
func (s *Server) handleCreateDownload(w http.ResponseWriter, _ *http.Request) { notImplemented(w) }
func (s *Server) handleGetDownload(w http.ResponseWriter, _ *http.Request)    { notImplemented(w) }
func (s *Server) handleDeleteDownload(w http.ResponseWriter, _ *http.Request) { notImplemented(w) }

// Watch Later
func (s *Server) handleGetWatchLater(w http.ResponseWriter, _ *http.Request)  { notImplemented(w) }
func (s *Server) handlePutWatchLater(w http.ResponseWriter, _ *http.Request)  { notImplemented(w) }
func (s *Server) handleWatchLaterAuth(w http.ResponseWriter, _ *http.Request) { notImplemented(w) }
func (s *Server) handleWatchLaterAuthCallback(w http.ResponseWriter, _ *http.Request) {
	notImplemented(w)
}

// Webhooks
func (s *Server) handleListWebhooks(w http.ResponseWriter, _ *http.Request)  { notImplemented(w) }
func (s *Server) handleCreateWebhook(w http.ResponseWriter, _ *http.Request) { notImplemented(w) }
func (s *Server) handleDeleteWebhook(w http.ResponseWriter, _ *http.Request) { notImplemented(w) }

// Jellyfin
func (s *Server) handleGetJellyfin(w http.ResponseWriter, _ *http.Request) { notImplemented(w) }
func (s *Server) handlePutJellyfin(w http.ResponseWriter, _ *http.Request) { notImplemented(w) }

// Admin
func (s *Server) handleAdminListUsers(w http.ResponseWriter, _ *http.Request)  { notImplemented(w) }
func (s *Server) handleAdminCreateUser(w http.ResponseWriter, _ *http.Request) { notImplemented(w) }
func (s *Server) handleAdminDeleteUser(w http.ResponseWriter, _ *http.Request) { notImplemented(w) }

// --- Helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func notImplemented(w http.ResponseWriter) {
	writeJSON(w, http.StatusNotImplemented, errBody("not implemented"))
}

func errBody(msg string) map[string]string {
	return map[string]string{"error": msg}
}

func newRequestID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
