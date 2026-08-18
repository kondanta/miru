-- Add playlist_id to watch_later_configs so users can configure which
-- YouTube playlist miru polls. The Watch Later playlist (WL) is not
-- accessible via the YouTube Data API v3 -- it returns 200 with empty
-- items regardless of content. Users must create a regular playlist and
-- paste its ID here. Poller skips users with no playlist_id set.
ALTER TABLE watch_later_configs ADD COLUMN playlist_id TEXT;
