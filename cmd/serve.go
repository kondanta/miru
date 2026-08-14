package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/kondanta/miru/internal/config"
	"github.com/kondanta/miru/internal/downloader"
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

	dl := downloader.New(cfg.DataDir, log)
	if err := dl.EnsureReady(ctx); err != nil {
		return fmt.Errorf("downloader: %w", err)
	}

	log.Info("miru ready", "addr", fmt.Sprintf(":%d", cfg.Port))
	<-ctx.Done()
	log.Info("shutting down")

	return nil
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
