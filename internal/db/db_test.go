package db

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"

	sqlite "modernc.org/sqlite"
)

// Extended SQLite result codes. modernc.org/sqlite always enables extended
// result codes via sqlite3_extended_result_codes, so Code() returns these.
const (
	sqliteConstraintForeignKey = 787 // SQLITE_CONSTRAINT_FOREIGNKEY
	sqliteConstraintCheck      = 275 // SQLITE_CONSTRAINT_CHECK
)

func requireSQLiteCode(t *testing.T, err error, wantCode int) {
	t.Helper()
	sqlErr, ok := errors.AsType[*sqlite.Error](err)
	if !ok {
		t.Fatalf("expected *sqlite.Error, got %T: %v", err, err)
	}
	if sqlErr.Code() != wantCode {
		t.Errorf("expected SQLite error code %d, got %d (%v)", wantCode, sqlErr.Code(), err)
	}
}

func openMigrated(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return db
}

func TestOpenAndMigrate(t *testing.T) {
	db := openMigrated(t)

	// Idempotency: second call must not error.
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatalf("Migrate (second call): %v", err)
	}

	tables := []string{
		"users",
		"youtube_tokens",
		"watch_later_configs",
		"downloads",
		"webhooks",
		"jellyfin_configs",
	}

	for _, tbl := range tables {
		t.Run(tbl, func(t *testing.T) {
			var n int
			err := db.QueryRowContext(
				t.Context(),
				"SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?",
				tbl,
			).Scan(&n)
			if err != nil {
				t.Fatalf("query sqlite_master: %v", err)
			}
			if n != 1 {
				t.Errorf("table %q missing after migration", tbl)
			}
		})
	}
}

func TestForeignKeysEnforced(t *testing.T) {
	db := openMigrated(t)

	_, err := db.ExecContext(
		t.Context(),
		`INSERT INTO downloads
		 (id, user_id, youtube_id, title, status, quality, sponsorblock, source, created_at, updated_at)
		 VALUES ('d1', 'no-such-user', 'yt1', 'title', 'queued', '1080p', 1, 'manual', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
	)
	if err == nil {
		t.Fatal("expected foreign-key violation, got nil")
	}
	requireSQLiteCode(t, err, sqliteConstraintForeignKey)
}

func TestStatusCheckConstraint(t *testing.T) {
	db := openMigrated(t)

	// Seed a user so the FK constraint doesn't fire before the CHECK.
	_, err := db.ExecContext(
		t.Context(),
		`INSERT INTO users (id, username, created_at) VALUES ('u1', 'testuser', '2026-01-01T00:00:00Z')`,
	)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}

	const insertDownload = `INSERT INTO downloads
		 (id, user_id, youtube_id, title, status, quality, sponsorblock, source, created_at, updated_at)
		 VALUES (?, 'u1', 'yt1', 'title', ?, '1080p', 1, 'manual', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`

	// All valid statuses must be accepted.
	for i, status := range []string{"queued", "downloading", "done", "failed", "deleted"} {
		t.Run("valid/"+status, func(t *testing.T) {
			id := fmt.Sprintf("d%d", i)
			_, err := db.ExecContext(t.Context(), insertDownload, id, status)
			if err != nil {
				t.Errorf("expected success for status %q, got: %v", status, err)
			}
		})
	}

	// An invalid status must be rejected with a CHECK constraint error.
	t.Run("invalid", func(t *testing.T) {
		_, err := db.ExecContext(t.Context(), insertDownload, "dinvalid", "invalid")
		if err == nil {
			t.Fatal("expected CHECK constraint violation, got nil")
		}
		requireSQLiteCode(t, err, sqliteConstraintCheck)
	})
}
