// Package db contains the SQLite database layer: migrations, sqlc-generated
// query types, and the database connection helper.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"

	_ "modernc.org/sqlite"
)

const driverName = "sqlite"

// Open opens (or creates) the SQLite database at path. Every connection in the
// pool is configured with foreign-key enforcement, WAL journal mode, and a
// busy-timeout so concurrent writers queue rather than fail immediately.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	dsn := "file:" + url.PathEscape(path) + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"

	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// SQLite allows only one writer at a time; a pool larger than one causes
	// "database is locked" errors under concurrent writes.
	db.SetMaxOpenConns(1)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	return db, nil
}
