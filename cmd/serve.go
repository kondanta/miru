package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/kondanta/miru/internal/downloader"
	"github.com/spf13/cobra"
)

func serveCmd() *cobra.Command {
	defaultDataDir := os.Getenv("MIRU_DATA_DIR")
	if defaultDataDir == "" {
		defaultDataDir = "./data"
	}

	var dataDir string

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the miru HTTP server",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return serve(dataDir)
		},
	}

	cmd.Flags().StringVar(&dataDir, "data-dir", defaultDataDir, "path to data directory (env: MIRU_DATA_DIR)")

	return cmd
}

func serve(dataDir string) error {
	log := newLogger()

	ctx, stop := appContext()
	defer stop()

	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	if err := initDownloader(ctx, dataDir, log); err != nil {
		return err
	}

	log.Info("miru ready", "data_dir", dataDir)
	<-ctx.Done()
	log.Info("shutting down")

	return nil
}

func newLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
}

func appContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func initDownloader(ctx context.Context, dataDir string, log *slog.Logger) error {
	dl := downloader.New(dataDir, log)
	if err := dl.EnsureReady(ctx); err != nil {
		return fmt.Errorf("downloader: %w", err)
	}

	return nil
}
