# miru

Self-hosted YouTube downloader with Jellyfin integration.

Paste a URL and miru downloads it via yt-dlp, generates NFO and poster files,
and serves the result to your Jellyfin library. Watched files are automatically
cleaned up after a configurable grace period. Optionally syncs your YouTube
Watch Later playlist on a schedule.

## Features

- Download YouTube videos via yt-dlp (auto-managed, no PATH dependency)
- Jellyfin integration — NFO/poster generation, watch status polling, auto-delete
- YouTube Watch Later sync via Google OAuth
- Per-user quality settings and SponsorBlock toggle
- Local auth or OIDC (Authentik and compatible providers)
- Webhook notifications on download events
- Single Docker container, SQLite, no external dependencies

## Quick start

```yaml
# docker-compose.yml
services:
  miru:
    image: ghcr.io/kondanta/miru:latest
    restart: unless-stopped
    ports:
      - "8090:8090"
    volumes:
      - ./data:/data
      - ./downloads:/downloads
    environment:
      MIRU_PORT: 8090
      MIRU_DATA_DIR: /data
      MIRU_DOWNLOADS_DIR: /downloads
```

```sh
docker compose up -d
```

Then open `http://localhost:8090`.

## Configuration

| Variable | Default | Description |
|---|---|---|
| `MIRU_PORT` | `8090` | HTTP listen port |
| `MIRU_DATA_DIR` | required | SQLite DB and yt-dlp binary cache |
| `MIRU_DOWNLOADS_DIR` | required | Downloaded files root |
| `MIRU_OIDC_ISSUER` | — | OIDC issuer URL (optional) |
| `MIRU_OIDC_CLIENT_ID` | — | OIDC client ID (optional) |
| `MIRU_OIDC_CLIENT_SECRET` | — | OIDC client secret (optional) |
| `MIRU_GOOGLE_CLIENT_ID` | — | Google OAuth client ID (Watch Later) |
| `MIRU_GOOGLE_CLIENT_SECRET` | — | Google OAuth client secret (Watch Later) |

## Development

Requires [mise](https://mise.jdx.dev).

```sh
mise install
mise run build    # build binary
mise run test     # run tests with race detector
mise run ci       # full check: build + test + vet + lint
```

See [AGENTS.md](AGENTS.md) for architecture details and development guidelines.

## License

[GPL-3.0](LICENSE) © 2026 Taylan Dogan
