package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

//go:embed migrations
var migrationFiles embed.FS

// Migrate runs all pending up migrations. It is idempotent: calling it when
// the schema is already current returns nil. Cancelling ctx signals a graceful
// stop; the migrator will finish any in-progress step and then return.
func Migrate(ctx context.Context, db *sql.DB) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}

	src, err := iofs.New(migrationFiles, "migrations")
	if err != nil {
		return fmt.Errorf("create migration source: %w", err)
	}

	driver, err := sqlite.WithInstance(db, &sqlite.Config{})
	if err != nil {
		return fmt.Errorf("create migration driver: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", src, "sqlite", driver)
	if err != nil {
		return fmt.Errorf("create migrator: %w", err)
	}

	stop := make(chan bool, 1)
	go func() {
		select {
		case <-ctx.Done():
			m.GracefulStop <- true
		case <-stop:
		}
	}()
	defer close(stop)

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("run migrations: %w", err)
	}

	// GracefulStop causes m.Up to return nil even when migrations were
	// interrupted; surface the cancellation so callers are not misled.
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}

	return nil
}
