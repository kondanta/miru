-- SQLite does not support DROP COLUMN before 3.35.0, and modernc.org/sqlite
-- bundles a recent enough version, but recreating the table is the safe
-- portable approach.
CREATE TABLE watch_later_configs_new (
    user_id               TEXT    NOT NULL PRIMARY KEY REFERENCES users (id),
    enabled               INTEGER NOT NULL DEFAULT 0,
    poll_interval_minutes INTEGER NOT NULL DEFAULT 10,
    last_polled           TEXT
);
INSERT INTO watch_later_configs_new (user_id, enabled, poll_interval_minutes, last_polled)
    SELECT user_id, enabled, poll_interval_minutes, last_polled FROM watch_later_configs;
DROP TABLE watch_later_configs;
ALTER TABLE watch_later_configs_new RENAME TO watch_later_configs;
