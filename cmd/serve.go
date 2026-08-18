package cmd

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/kondanta/miru/internal/auth"
	"github.com/kondanta/miru/internal/config"
	"github.com/kondanta/miru/internal/db"
	"github.com/kondanta/miru/internal/downloader"
	"github.com/kondanta/miru/internal/nfo"
	"github.com/kondanta/miru/internal/queue"
	"github.com/kondanta/miru/internal/server"
	"github.com/kondanta/miru/web"
	"github.com/spf13/cobra"
)

func serveCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Start the miru HTTP server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(cmd.Context(), *configPath)
			if err != nil {
				return err
			}
			return serve(cmd.Context(), cfg)
		},
	}
}

func serve(ctx context.Context, cfg *config.Config) error {
	log := newLogger(cfg.LogLevel)

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	if err := os.MkdirAll(cfg.DownloadsDir, 0o750); err != nil {
		return fmt.Errorf("create downloads dir: %w", err)
	}

	database, err := openDB(ctx, cfg.DataDir)
	if err != nil {
		return err
	}
	defer func() { _ = database.Close() }()

	dl := downloader.New(cfg.DataDir, log)
	if err := dl.EnsureReady(ctx); err != nil {
		return fmt.Errorf("downloader: %w", err)
	}
	defer dl.Close()

	if err := bootstrapAdmin(ctx, database, log); err != nil {
		return err
	}

	queueMgr := queue.New(ctx, newWorker(database, dl, cfg, log), log)

	if err := reconcileDownloads(ctx, database, queueMgr, log); err != nil {
		return fmt.Errorf("reconcile downloads: %w", err)
	}

	srv := server.New(database, cfg, queueMgr, log, web.DistDirFS)

	httpSrv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ln, err := net.Listen("tcp", httpSrv.Addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	go func() {
		if err := httpSrv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server error", "err", err)
		}
	}()

	log.Info("miru ready", "addr", httpSrv.Addr)
	<-ctx.Done()
	log.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Error("http shutdown error", "err", err)
	}

	queueMgr.Wait()

	return nil
}

func openDB(ctx context.Context, dataDir string) (*sql.DB, error) {
	database, err := db.Open(ctx, filepath.Join(dataDir, "miru.db"))
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := db.Migrate(ctx, database); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	return database, nil
}

// bootstrapAdmin creates the first admin user from MIRU_ADMIN_USERNAME and
// MIRU_ADMIN_PASSWORD env vars when no users exist in the database. It is a
// no-op when either env var is absent or the users table is non-empty.
func bootstrapAdmin(ctx context.Context, database *sql.DB, log *slog.Logger) error {
	username := os.Getenv("MIRU_ADMIN_USERNAME")
	password := os.Getenv("MIRU_ADMIN_PASSWORD")
	if username == "" || password == "" {
		return nil
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return fmt.Errorf("bootstrap: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	// Single atomic statement: inserts only when no users exist.
	// Safe under concurrent startup — the second process is a no-op.
	result, err := database.ExecContext(ctx,
		`INSERT INTO users (id, username, password, is_admin, created_at)
		 SELECT ?, ?, ?, 1, ? WHERE NOT EXISTS (SELECT 1 FROM users)`,
		uuid.NewString(), username, hash, now,
	)
	if err != nil {
		return fmt.Errorf("bootstrap: %w", err)
	}

	if n, _ := result.RowsAffected(); n > 0 {
		log.Info("admin user created", "username", username)
	}
	return nil
}

// reconcileDownloads re-enqueues rows left in 'queued' state and resets
// 'downloading' rows back to 'queued' before re-enqueuing them. Both states
// indicate work that was interrupted by a crash or restart and should be retried.
// The video URL is reconstructed from the stored youtube_id.
func reconcileDownloads(ctx context.Context, database *sql.DB, q *queue.Manager, log *slog.Logger) error {
	// Reset any rows stuck in 'downloading' — the worker was interrupted mid-flight.
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := database.ExecContext(ctx,
		`UPDATE downloads SET status='queued', updated_at=? WHERE status='downloading'`, now,
	); err != nil {
		return fmt.Errorf("reset downloading rows: %w", err)
	}

	rows, err := database.QueryContext(ctx,
		`SELECT id, user_id, youtube_id, quality, sponsorblock FROM downloads WHERE status='queued'`,
	)
	if err != nil {
		return fmt.Errorf("query queued downloads: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var count int
	for rows.Next() {
		var (
			id, userID, youtubeID, quality string
			sponsorblock                   int
		)
		if err := rows.Scan(&id, &userID, &youtubeID, &quality, &sponsorblock); err != nil {
			return fmt.Errorf("scan queued download: %w", err)
		}
		q.Enqueue(queue.Job{
			ID:           id,
			UserID:       userID,
			YoutubeID:    youtubeID,
			URL:          "https://www.youtube.com/watch?v=" + youtubeID,
			Quality:      quality,
			SponsorBlock: sponsorblock == 1,
		})
		count++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate queued downloads: %w", err)
	}

	if count > 0 {
		log.Info("reconciled downloads on startup", "count", count)
	}
	return nil
}

// newWorker returns the WorkerFunc that drives the full download pipeline:
// yt-dlp → NFO generation → poster download → DB status update.
func newWorker(database *sql.DB, dl *downloader.Manager, cfg *config.Config, log *slog.Logger) queue.WorkerFunc {
	return func(ctx context.Context, job queue.Job) error {
		now := time.Now().UTC().Format(time.RFC3339)
		if _, err := database.ExecContext(ctx,
			`UPDATE downloads SET status='downloading', updated_at=? WHERE id=?`, now, job.ID,
		); err != nil {
			markFailed(database, job.ID, log)
			return fmt.Errorf("worker: mark downloading: %w", err)
		}

		if err := runDownload(ctx, database, dl, cfg, log, job); err != nil {
			log.Error("download failed", "job_id", job.ID, "err", err)
			markFailed(database, job.ID, log)
			return err
		}
		return nil
	}
}

func runDownload(
	ctx context.Context,
	database *sql.DB,
	dl *downloader.Manager,
	cfg *config.Config,
	log *slog.Logger,
	job queue.Job,
) error {
	outDir := filepath.Join(cfg.DownloadsDir, job.ID)
	if err := os.MkdirAll(outDir, 0o750); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	var stderrBuf bytes.Buffer
	opts := downloader.DownloadOpts{
		Quality:       job.Quality,
		SponsorBlock:  job.SponsorBlock,
		WriteInfoJSON: true,
		Stderr:        &stderrBuf,
	}
	dlCtx, dlCancel := context.WithTimeout(ctx, 6*time.Hour)
	dlErr := dl.Download(dlCtx, job.URL, outDir, opts, io.Discard)
	dlCancel()
	if dlErr != nil {
		log.Error("yt-dlp failed", "job_id", job.ID, "stderr", stderrBuf.String())
		return dlErr
	}

	infoPath, err := findInfoJSON(outDir)
	if err != nil {
		return fmt.Errorf("find info.json: %w", err)
	}

	infoFile, err := os.Open(infoPath)
	if err != nil {
		return fmt.Errorf("open info.json: %w", err)
	}
	info, parseErr := nfo.Parse(infoFile)
	_ = infoFile.Close()
	if parseErr != nil {
		return fmt.Errorf("parse info.json: %w", parseErr)
	}

	nfoPath := strings.TrimSuffix(infoPath, ".info.json") + ".nfo"
	nfoFile, err := os.Create(nfoPath)
	if err != nil {
		return fmt.Errorf("create nfo file: %w", err)
	}
	writeErr := nfo.WriteNFO(nfoFile, info, *cfg.NFO.MaxTags)
	_ = nfoFile.Close()
	if writeErr != nil {
		return fmt.Errorf("write nfo: %w", writeErr)
	}

	posterPath := filepath.Join(outDir, "poster.jpg")
	posterFile, err := os.Create(posterPath)
	if err != nil {
		return fmt.Errorf("create poster file: %w", err)
	}
	posterCtx, posterCancel := context.WithTimeout(ctx, 30*time.Second)
	fetchErr := nfo.FetchPoster(posterCtx, posterFile, info, nil, int64(*cfg.NFO.MaxPosterBytes))
	posterCancel()
	_ = posterFile.Close()
	if fetchErr != nil {
		_ = os.Remove(posterPath)
		log.Warn("fetch poster failed (non-fatal)", "job_id", job.ID, "err", fetchErr)
	}

	videoPath, err := findVideoFile(outDir)
	if err != nil {
		return fmt.Errorf("find video file: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := database.ExecContext(ctx,
		`UPDATE downloads SET status='done', title=?, file_path=?, updated_at=? WHERE id=?`,
		info.Title, videoPath, now, job.ID,
	); err != nil {
		return fmt.Errorf("mark done: %w", err)
	}

	log.Info("download complete", "job_id", job.ID, "title", info.Title)
	return nil
}

// markFailed updates the download status to "failed" using a fresh context so
// that it succeeds even when the job context was already cancelled.
func markFailed(database *sql.DB, downloadID string, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := database.ExecContext(ctx,
		`UPDATE downloads SET status='failed', updated_at=? WHERE id=?`, now, downloadID,
	); err != nil {
		log.Error("mark download failed: db error", "download_id", downloadID, "err", err)
	}
}

func findInfoJSON(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read dir: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".info.json") {
			return filepath.Join(dir, e.Name()), nil
		}
	}
	return "", fmt.Errorf("no .info.json file found in %s", dir)
}

func findVideoFile(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read dir: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".mp4", ".mkv", ".webm":
			return filepath.Join(dir, e.Name()), nil
		}
	}
	return "", fmt.Errorf("no video file found in %s", dir)
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	switch level {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: l}))
}
