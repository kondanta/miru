package db

import (
	"testing"
)

func TestOpenAndMigrate(t *testing.T) {
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

func TestStatusCheckConstraint(t *testing.T) {
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

	// Seed a user so the FK constraint doesn't fire first.
	_, err = db.ExecContext(
		t.Context(),
		`INSERT INTO users (id, username, created_at) VALUES ('u1', 'testuser', '2026-01-01T00:00:00Z')`,
	)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}

	// An invalid status must be rejected.
	_, err = db.ExecContext(t.Context(),
		`INSERT INTO downloads
		 (id, user_id, youtube_id, title, status, quality, sponsorblock, source, created_at, updated_at)
		 VALUES ('d1', 'u1', 'yt1', 'title', 'invalid', '1080p', 1, 'manual', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	if err == nil {
		t.Error("expected CHECK constraint violation for invalid status, got nil")
	}
}
