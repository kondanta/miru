# Repository Agent Guide

This file provides guidance to AI coding agents working on this repository.
`CLAUDE.md` references this file. Read it fully before touching any file.

---

## What is miru?

miru is a self-hosted YouTube downloader with Jellyfin integration. Single
Docker container: Go backend, Vue 3 SPA embedded in the binary, SQLite.
Users paste a URL (or enable Watch Later polling) and miru downloads via
yt-dlp, generates NFO/poster files for Jellyfin, and auto-deletes watched
files after a configurable grace period.

Module path: `github.com/kondanta/miru`

---

## Development Commands

All tasks run through mise. Check available tasks with `mise task ls`.

| Command | What it does |
|---|---|
| `mise run build` | `go build ./...` |
| `mise run test` | `go test -race ./...` |
| `mise run test-fresh` | `go test -race -count=1 ./...` (bypasses cache) |
| `mise run vet` | `go vet ./...` |
| `mise run lint` | `golangci-lint run` |
| `mise run ci` | build + test + vet + lint |
| `mise run ci-fresh` | same, cache-busted |
| `mise run zizmor` | scan `.github/workflows/` for security issues |
| `mise run build-ui` | placeholder — frontend path not yet decided |

Run `mise run ci` (not a bare `go test`) before declaring any task done.
`ci` includes the race detector; bare `go test` does not.

---

## Project Structure

```
miru/
├── main.go                      # Entrypoint only. Calls cmd.Execute().
├── cmd/
│   ├── root.go                  # Execute(), root cobra command, version flag
│   └── serve.go                 # `miru serve` subcommand
├── internal/
│   ├── config/                  # TOML + env config loading and validation
│   ├── db/                      # SQLite connection, migrations, sqlc queries
│   ├── downloader/              # yt-dlp binary lifecycle: download, cache,
│   │                            #   update, smoke test, rollback
│   ├── queue/                   # Per-user download queue (one goroutine/user)
│   ├── server/                  # HTTP router, middleware, handlers
│   ├── auth/                    # Local (bcrypt+JWT) and OIDC auth
│   ├── youtube/                 # YouTube Data API v3, Google OAuth PKCE,
│   │                            #   Watch Later cron poller
│   ├── jellyfin/                # Jellyfin API client + three background jobs
│   ├── nfo/                     # NFO + poster generation from yt-dlp JSON
│   └── webhook/                 # Outgoing HTTP webhook notifications
├── web/
│   ├── embed.go                 # //go:embed all:dist, exports DistDirFS
│   └── dist/                    # Vite build output (gitignored, .gitkeep only)
├── internal/db/migrations/      # SQL migration files (golang-migrate)
├── docs/
│   └── docs/ARCHITECTURE.md          # Authoritative design doc — read before
│                                #   implementing any subsystem
├── AGENTS.md                    # This file
├── CLAUDE.md                    # Points here
├── .mise/config.toml            # Tool versions + task definitions
├── .goreleaser.yaml             # Multi-arch release builds
├── Dockerfile                   # Runtime image (no yt-dlp — app-managed)
└── docker-compose.yml
```

`internal/` is intentional. Nothing inside it is importable by external packages.
Do not create packages outside this structure without asking first.

---

## Language and Runtime

Go 1.26.5. Use all language features available in that version.

### Error handling

```go
// preferred (Go 1.26)
if netErr, ok := errors.AsType[*net.OpError](err); ok { ... }

// avoid (pre-1.26 style)
var netErr *net.OpError
if errors.As(err, &netErr) { ... }
```

Use `errors.Is` for sentinel comparison. Wrap with `fmt.Errorf("context: %w", err)`.
Never log and return — pick one. Never swallow errors silently.

### Pointer initialisation

```go
// preferred (Go 1.26)
enabled := new(true)
port := new(8090)

// avoid
v := true; enabled := &v
```

### Logging

`log/slog` everywhere. `slog.NewMultiHandler` when fanning out to multiple sinks.
No `fmt.Println` or `log.Printf` in `internal/` — only CLI and server layers print.

### Context

Propagate `context.Context` through every call chain. No `context.Background()`
in library code below the entrypoint.

### Testing

Table-driven tests with `t.Run`. Use `testing/synctest` for concurrent or
timing-sensitive tests. Race detector is not optional — `mise run test` enables it.

---

## Approved Dependencies

Do not `go get` anything not on this list without asking first.

| Package | Purpose |
|---|---|
| `github.com/spf13/cobra` | CLI framework |
| `github.com/go-chi/chi/v5` | HTTP router |
| `github.com/BurntSushi/toml` | Config parsing |
| `modernc.org/sqlite` | Pure-Go SQLite driver (CGO_ENABLED=0) |
| `github.com/golang-migrate/migrate/v4` | SQL schema migrations |
| `github.com/sqlc-dev/sqlc` | Query generation (dev tool, not runtime) |
| `github.com/golang-jwt/jwt/v5` | JWT session tokens |
| `github.com/lmittmann/tint` | Coloured slog handler for dev output |

If you think a new dependency is justified, stop and ask. Do not `go get` it first.

---

## Database

Schema lives in `docs/ARCHITECTURE.md` (authoritative) and is reflected in
`internal/db/migrations/`. Tables: `users`, `youtube_tokens`,
`watch_later_configs`, `downloads`, `webhooks`, `jellyfin_configs`.

Rules:
- Every schema change requires a new numbered migration file in `internal/db/migrations/`.
- Never edit an existing migration — add a new one.
- sqlc-generated code lives in `internal/db/` and is re-generated via `sqlc generate`.
- All timestamps stored as RFC3339 TEXT. No Unix integers.
- Download status values: `queued | downloading | done | failed | deleted` — do
  not invent new values.

---

## Key Subsystem Rules

### yt-dlp (`internal/downloader`)

**yt-dlp is never installed as a system package.** The app downloads, caches,
updates, and invokes it entirely within the data directory (`MIRU_DATA_DIR`).
Never assume `yt-dlp` is on `PATH`. Never `apk add yt-dlp` or equivalent.

Update flow: startup check → async fetch latest GitHub release → compare
version → replace binary → smoke test → rollback if test fails.

### Download Queue (`internal/queue`)

One goroutine per user. Jobs are processed sequentially within each user's queue.
Job state machine: `queued → downloading → done | failed`.
Architecture supports configurable per-user concurrency but it is not wired in MVP.

### Auth (`internal/auth`)

Two independent OAuth flows — do not conflate them:

| Flow | Purpose | Provider |
|---|---|---|
| App login (local) | Identify user in miru | miru itself (bcrypt + JWT) |
| App login (OIDC) | Identify user in miru | Authentik / any OIDC provider |
| YouTube API OAuth | Watch Later access + item deletion | Google |

A user can log into miru via Authentik and still need to separately authorise
Google for Watch Later. OIDC `openid profile email` scope is for app login only.
YouTube OAuth scope is `https://www.googleapis.com/auth/youtube`.

### Jellyfin (`internal/jellyfin`)

Three background jobs — all three must remain independent:

1. **Reconciliation** (startup + hourly): match Jellyfin library items to
   `downloads` by filename, store `jellyfin_item_id`.
2. **Watch status poller**: for downloads with a `jellyfin_item_id` and no
   `watched_at`, call Jellyfin user-data API; on watched, set `watched_at`
   and compute `delete_after = watched_at + grace_hours`.
3. **Cleanup**: delete files and mark records `deleted` where `delete_after < now()`.

Jellyfin library type is **Movies** (flat directory per user).

### NFO Generation (`internal/nfo`)

Triggered after yt-dlp completes. Reads `--write-info-json` output.
Output filenames: `YYYY-MM-DD - {title}.nfo` and `YYYY-MM-DD - {title}-poster.jpg`.
File layout on disk: `/downloads/{username}/YYYY-MM-DD - {title}.{ext}`.

---

## What You Must Never Do Without Asking

- Add a dependency not on the approved list
- Create packages outside the defined structure
- Modify an existing migration file (always add a new one)
- Touch `web/dist/` — it is a build artefact
- Assume `yt-dlp` is on `PATH` or installed as a system package
- Conflate app OIDC login with YouTube API OAuth
- Change the download status enum values
- Rename the binary or any top-level command
- Change the on-disk file naming convention (`YYYY-MM-DD - {title}.{ext}`)

---

## Definition of Done

A task is complete when:

1. `mise run ci` passes (build + test with race detector + vet + lint)
2. New exported symbols have GoDoc comments
3. No new dependencies were added without approval
4. Any new migration file is numbered sequentially and non-destructive
5. `docs/ARCHITECTURE.md` is updated if a subsystem design changed
