// Package server wires the HTTP router, middleware, and handlers.
package server

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/kondanta/miru/internal/auth"
	"github.com/kondanta/miru/internal/config"
	"github.com/kondanta/miru/internal/downloader"
	"github.com/kondanta/miru/internal/queue"
)

type contextKey int

const (
	keyRequestID contextKey = iota
	keyClaims
)

const jwtTTL = 24 * time.Hour

// dummyHash is computed once at startup and used to normalize login response
// timing when the requested username does not exist, preventing user
// enumeration via response-time differences.
var dummyHash = func() string {
	h, err := auth.HashPassword("miru-dummy-password-for-timing-normalization")
	if err != nil {
		panic(fmt.Errorf("dummyHash init: %w", err))
	}
	return h
}()

// loginLimiter is a per-username fixed-window rate limiter for the login
// endpoint. Keying by username means per-account throttling holds regardless of
// how many source IPs an attacker rotates through.
//
// The map is capped at maxBuckets entries. When the cap is full a new username
// is rejected with 429 until an existing bucket expires or a successful login
// clears one. This is an acknowledged DoS tradeoff: an attacker can fill the
// cap with 10 bogus usernames, temporarily blocking new login attempts. At
// personal-deployment scale this is acceptable.
type loginLimiter struct {
	mu          sync.Mutex
	buckets     map[string]loginBucket
	maxAttempts int
	maxBuckets  int
	window      time.Duration
}

type loginBucket struct {
	count   int
	resetAt time.Time
}

func newLoginLimiter(maxAttempts, maxBuckets int, window time.Duration) *loginLimiter {
	return &loginLimiter{
		buckets:     make(map[string]loginBucket),
		maxAttempts: maxAttempts,
		maxBuckets:  maxBuckets,
		window:      window,
	}
}

// isLimited reports whether username may proceed. It opportunistically removes
// expired buckets on each call, which naturally frees cap slots over time.
func (l *loginLimiter) isLimited(username string) (limited bool, retryAfter int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()

	// Opportunistic expiry sweep — bounded by maxBuckets so always O(maxBuckets).
	for k, b := range l.buckets {
		if now.After(b.resetAt) {
			delete(l.buckets, k)
		}
	}

	b, exists := l.buckets[username]
	if !exists {
		if len(l.buckets) >= l.maxBuckets {
			// Cap full — reject until a slot opens.
			return true, int(l.window.Seconds())
		}
		return false, 0
	}
	if b.count >= l.maxAttempts {
		return true, int(b.resetAt.Sub(now).Seconds()) + 1
	}
	return false, 0
}

// recordFailure increments the failure count for username.
func (l *loginLimiter) recordFailure(username string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	b := l.buckets[username]
	if now.After(b.resetAt) {
		b = loginBucket{resetAt: now.Add(l.window)}
	}
	b.count++
	l.buckets[username] = b
}

// clearFailures removes the bucket for username after a successful login so
// that legitimate repeated logins are never penalised and the slot is freed.
func (l *loginLimiter) clearFailures(username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.buckets, username)
}

// Server holds shared dependencies for all HTTP handlers.
type Server struct {
	db           *sql.DB
	cfg          *config.Config
	dl           *downloader.Manager
	queue        *queue.Manager
	log          *slog.Logger
	web          fs.FS
	loginLimiter *loginLimiter
}

// New creates a Server. web is the embedded SPA filesystem (may be nil to
// disable the catch-all SPA route during tests).
func New(
	db *sql.DB,
	cfg *config.Config,
	dl *downloader.Manager,
	q *queue.Manager,
	log *slog.Logger,
	web fs.FS,
) *Server {
	return &Server{
		db:           db,
		cfg:          cfg,
		dl:           dl,
		queue:        q,
		log:          log,
		web:          web,
		loginLimiter: newLoginLimiter(10, 10, 10*time.Minute),
	}
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

		// OAuth callback — Google redirects the browser here without a JWT.
		// The user is identified via the state parameter set in handleWatchLaterAuth.
		r.Get("/watch-later/auth/callback", s.handleWatchLaterAuthCallback)

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
			r.Delete("/watch-later", s.handleDeleteWatchLater)
			r.Get("/watch-later/auth", s.handleWatchLaterAuth)

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
				r.Put("/admin/cookies", s.handleAdminPutCookies)
				r.Delete("/admin/cookies", s.handleAdminDeleteCookies)
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

// authenticate verifies the Bearer JWT, checks token_version against the DB,
// and injects the claims into the request context.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			writeJSON(w, http.StatusUnauthorized, errBody("missing or malformed Authorization header"))
			return
		}

		claims, err := auth.ParseToken(strings.TrimPrefix(header, "Bearer "), []byte(s.cfg.JWTSecret))
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, errBody("invalid token"))
			return
		}

		// Verify token_version to support per-user session invalidation.
		var dbVersion int
		err = s.db.QueryRowContext(r.Context(),
			`SELECT token_version FROM users WHERE id = ?`, claims.Subject).
			Scan(&dbVersion)
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusUnauthorized, errBody("user not found"))
			return
		}
		if err != nil {
			s.log.Error("token_version lookup", "err", err)
			writeJSON(w, http.StatusInternalServerError, errBody("internal server error"))
			return
		}
		if claims.TokenVersion != dbVersion {
			writeJSON(w, http.StatusUnauthorized, errBody("token has been invalidated"))
			return
		}

		ctx := context.WithValue(r.Context(), keyClaims, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// adminOnly rejects requests where the JWT claims do not carry is_admin=true.
func (s *Server) adminOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, ok := r.Context().Value(keyClaims).(*auth.Claims)
		if !ok || !claims.IsAdmin {
			writeJSON(w, http.StatusForbidden, errBody("forbidden"))
			return
		}
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

// --- Infrastructure handlers ---

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// --- Stub handlers (subsystems not yet implemented) ---

// Webhooks
func (s *Server) handleListWebhooks(w http.ResponseWriter, _ *http.Request)  { notImplemented(w) }
func (s *Server) handleCreateWebhook(w http.ResponseWriter, _ *http.Request) { notImplemented(w) }
func (s *Server) handleDeleteWebhook(w http.ResponseWriter, _ *http.Request) { notImplemented(w) }

// Jellyfin
func (s *Server) handleGetJellyfin(w http.ResponseWriter, _ *http.Request) { notImplemented(w) }
func (s *Server) handlePutJellyfin(w http.ResponseWriter, _ *http.Request) { notImplemented(w) }

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

// claimsFrom extracts JWT claims from the request context. On failure it writes
// a 401 response and returns false; the caller must return immediately.
func claimsFrom(w http.ResponseWriter, r *http.Request) (*auth.Claims, bool) {
	claims, ok := r.Context().Value(keyClaims).(*auth.Claims)
	if !ok || claims == nil {
		writeJSON(w, http.StatusUnauthorized, errBody("unauthorized"))
		return nil, false
	}
	return claims, true
}
