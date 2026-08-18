-- Restore watch_later_configs to its original shape.
CREATE TABLE watch_later_configs_old (
    user_id   TEXT    NOT NULL PRIMARY KEY REFERENCES users (id),
    enabled   INTEGER NOT NULL DEFAULT 0,
    poll_cron TEXT    NOT NULL DEFAULT '*/15 * * * *'
);
INSERT INTO watch_later_configs_old (user_id, enabled)
    SELECT user_id, enabled FROM watch_later_configs;
DROP TABLE watch_later_configs;
ALTER TABLE watch_later_configs_old RENAME TO watch_later_configs;

-- SQLite does not support DROP COLUMN for older versions; the columns added
-- to downloads (playlist_item_id, wl_retry_count) are left in place on down.
