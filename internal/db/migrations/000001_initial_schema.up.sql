CREATE TABLE users (
    id           TEXT PRIMARY KEY,
    username     TEXT UNIQUE NOT NULL,
    password     TEXT,
    oidc_sub     TEXT UNIQUE,
    quality      TEXT    NOT NULL DEFAULT '1080p',
    sponsorblock INTEGER NOT NULL DEFAULT 1,
    grace_hours  INTEGER NOT NULL DEFAULT 24,
    created_at   TEXT    NOT NULL
);

CREATE TABLE youtube_tokens (
    user_id       TEXT NOT NULL PRIMARY KEY REFERENCES users (id),
    access_token  TEXT NOT NULL,
    refresh_token TEXT NOT NULL,
    expiry        TEXT NOT NULL
);

CREATE TABLE watch_later_configs (
    user_id   TEXT    NOT NULL PRIMARY KEY REFERENCES users (id),
    enabled   INTEGER NOT NULL DEFAULT 0,
    poll_cron TEXT    NOT NULL DEFAULT '*/15 * * * *'
);

CREATE TABLE downloads (
    id               TEXT PRIMARY KEY,
    user_id          TEXT    NOT NULL REFERENCES users (id),
    youtube_id       TEXT    NOT NULL,
    title            TEXT    NOT NULL,
    file_path        TEXT,
    status           TEXT    NOT NULL CHECK (status IN ('queued', 'downloading', 'done', 'failed', 'deleted')),
    quality          TEXT    NOT NULL,
    sponsorblock     INTEGER NOT NULL,
    source           TEXT    NOT NULL,
    jellyfin_item_id TEXT,
    watched_at       TEXT,
    delete_after     TEXT,
    created_at       TEXT    NOT NULL,
    updated_at       TEXT    NOT NULL
);

CREATE INDEX downloads_user_id ON downloads (user_id);
CREATE INDEX downloads_status  ON downloads (status);

CREATE TABLE webhooks (
    id      TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users (id),
    url     TEXT NOT NULL,
    events  TEXT NOT NULL
);

CREATE TABLE jellyfin_configs (
    user_id    TEXT NOT NULL PRIMARY KEY REFERENCES users (id),
    url        TEXT NOT NULL,
    api_key    TEXT NOT NULL,
    library_id TEXT NOT NULL
);
