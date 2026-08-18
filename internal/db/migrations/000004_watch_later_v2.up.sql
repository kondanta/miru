-- Recreate watch_later_configs: replace poll_cron (cron string) with
-- poll_interval_minutes (positive integer, range [1, 4320]) and add
-- last_polled to drive the per-user interval check in the poller.
CREATE TABLE watch_later_configs_new (
    user_id               TEXT    NOT NULL PRIMARY KEY REFERENCES users (id),
    enabled               INTEGER NOT NULL DEFAULT 0,
    poll_interval_minutes INTEGER NOT NULL DEFAULT 10,
    last_polled           TEXT
);
INSERT INTO watch_later_configs_new (user_id, enabled)
    SELECT user_id, enabled FROM watch_later_configs;
DROP TABLE watch_later_configs;
ALTER TABLE watch_later_configs_new RENAME TO watch_later_configs;

-- Add Watch Later columns to downloads:
--   playlist_item_id: YouTube playlistItems resource ID, set for WL-sourced
--                     downloads; used to remove the item from WL after success.
--   wl_retry_count:   number of failed download attempts for WL-sourced items;
--                     poller stops retrying at 5. UI shows an alert when >= 5.
ALTER TABLE downloads ADD COLUMN playlist_item_id TEXT;
ALTER TABLE downloads ADD COLUMN wl_retry_count INTEGER NOT NULL DEFAULT 0;
