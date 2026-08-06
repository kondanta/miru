package db

import (
	"testing"
)

func TestOpenAndMigrate(t *testing.T) {
	db, err := Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Idempotency: second call must not error.
	if err := Migrate(db); err != nil {
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
	db, err := Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Inserting a download with a non-existent user_id must fail.
	_, err = db.ExecContext(
		t.Context(),
		`INSERT INTO downloads
		 (id, user_id, youtube_id, title, status, quality, sponsorblock, source, created_at, updated_at)
		 VALUES ('d1', 'no-such-user', 'yt1', 'title', 'queued', '1080p', 1, 'manual', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
	)
	if err == nil {
		t.Error("expected foreign-key violation, got nil")
	}
}
