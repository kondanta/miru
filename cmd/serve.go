package cmd

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/kondanta/miru/internal/config"
	"github.com/kondanta/miru/internal/db"
	"github.com/kondanta/miru/internal/downloader"
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

	queueMgr := queue.New(ctx, stubWorker(log), log)

	srv := server.New(database, cfg, queueMgr, log, web.DistDirFS)

	httpSrv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		if err := httpSrv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
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

// stubWorker is a placeholder until the real download worker is implemented.
// TODO: replace with downloader invocation + nfo generation + jellyfin reconciliation.
func stubWorker(log *slog.Logger) queue.WorkerFunc {
	return func(_ context.Context, job queue.Job) error {
		log.Info("download job received (stub)", "job_id", job.ID, "url", job.URL)
		return nil
	}
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
