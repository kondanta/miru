package youtube

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/kondanta/miru/internal/queue"
	"golang.org/x/oauth2"
)

const (
	// MaxWLRetries is the number of failed download attempts after which the
	// poller stops retrying a WL-sourced item. The item remains in the user's
	// real YouTube WL playlist. The UI shows an alert for exhausted items
	// (wl_retry_count >= MaxWLRetries).
	MaxWLRetries = 5

	// pollerTick is the base interval at which the poller wakes to check each
	// user's per-user poll interval. Short so per-user intervals near 1 minute
	// are honoured accurately.
	pollerTick = time.Minute
)

// PollDeps groups the dependencies the Watch Later poller needs.
type PollDeps struct {
	DB       *sql.DB
	EncKey   []byte
	OAuthCfg *oauth2.Config
	Queue    *queue.Manager
	Log      *slog.Logger
}

// RunPoller starts the Watch Later poller. It ticks every minute, checks each
// enabled user whose poll interval has elapsed, fetches their WL playlist, and
// enqueues new items. Call in a goroutine; returns when ctx is cancelled.
func RunPoller(ctx context.Context, d PollDeps) {
	ticker := time.NewTicker(pollerTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pollAll(ctx, d)
		}
	}
}

type pollCandidate struct {
	userID      string
	intervalMin int
	lastPolled  time.Time
	quality     string
	playlistID  string
}

// pollAll iterates enabled users and polls each one whose interval has elapsed.
// Rows are drained into a slice before processing so the DB cursor (and its
// connection) is closed before pollUser makes additional DB calls.
// This avoids a deadlock: the pool has MaxOpenConns=1, so leaving rows open
// while calling pollUser would block all further DB operations until timeout.
func pollAll(ctx context.Context, d PollDeps) {
	rows, err := d.DB.QueryContext(ctx, `
		SELECT wlc.user_id, wlc.poll_interval_minutes, wlc.last_polled,
		       u.quality, wlc.playlist_id
		FROM watch_later_configs wlc
		JOIN users u ON u.id = wlc.user_id
		WHERE wlc.enabled = 1 AND wlc.playlist_id IS NOT NULL AND wlc.playlist_id != ''`)
	if err != nil {
		d.Log.Error("wl poller: query users", "err", err)
		return
	}

	now := time.Now().UTC()
	var candidates []pollCandidate
	func() {
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var (
				userID        string
				intervalMin   int
				lastPolledStr sql.NullString
				quality       string
				playlistID    string
			)
			if err := rows.Scan(&userID, &intervalMin, &lastPolledStr, &quality, &playlistID); err != nil {
				d.Log.Error("wl poller: scan user", "err", err)
				continue
			}
			var lastPolled time.Time
			if lastPolledStr.Valid && lastPolledStr.String != "" {
				lastPolled, _ = time.Parse(time.RFC3339, lastPolledStr.String)
			}
			if now.Sub(lastPolled) >= time.Duration(intervalMin)*time.Minute {
				candidates = append(candidates, pollCandidate{userID, intervalMin, lastPolled, quality, playlistID})
			}
		}
		if err := rows.Err(); err != nil {
			d.Log.Error("wl poller: iterate users", "err", err)
		}
	}()

	for _, c := range candidates {
		pollUser(ctx, d, c.userID, c.quality, c.playlistID, now)
	}
}

func pollUser(ctx context.Context, d PollDeps, userID, quality, playlistID string, now time.Time) {
	tok, err := LoadToken(ctx, d.DB, d.EncKey, userID)
	if errors.Is(err, sql.ErrNoRows) {
		// User has no token stored — disable WL so we stop polling them.
		_, _ = d.DB.ExecContext(ctx,
			`UPDATE watch_later_configs SET enabled=0 WHERE user_id=?`, userID)
		return
	}
	if errors.Is(err, ErrKeyMismatch) {
		d.Log.Warn("wl poller: token decrypt failed (key rotated?), disabling user", "user_id", userID)
		_, _ = d.DB.ExecContext(ctx,
			`UPDATE watch_later_configs SET enabled=0 WHERE user_id=?`, userID)
		return
	}
	if err != nil {
		d.Log.Error("wl poller: load token", "user_id", userID, "err", err)
		return
	}

	apiCtx, apiCancel := context.WithTimeout(ctx, 30*time.Second)
	client := TokenClient(apiCtx, d.OAuthCfg, tok)
	items, err := ListWatchLater(apiCtx, client, playlistID)
	apiCancel()
	if err != nil {
		d.Log.Error("wl poller: list playlist", "user_id", userID, "playlist_id", playlistID, "err", err)
		return
	}

	d.Log.Info("wl poller: fetched playlist", "user_id", userID, "playlist_id", playlistID, "items", len(items))

	// Update last_polled regardless of how many items were enqueued.
	_, _ = d.DB.ExecContext(ctx,
		`UPDATE watch_later_configs SET last_polled=? WHERE user_id=?`,
		now.Format(time.RFC3339), userID,
	)

	for _, item := range items {
		queued, err := enqueueIfNew(ctx, d, userID, quality, item)
		switch {
		case err != nil:
			d.Log.Error("wl poller: enqueue", "user_id", userID, "video_id", item.VideoID, "err", err)
		case queued:
			d.Log.Info("wl poller: queued", "user_id", userID, "video_id", item.VideoID, "title", item.Title)
		default:
			d.Log.Debug("wl poller: skipped", "user_id", userID, "video_id", item.VideoID)
		}
	}
}

// enqueueIfNew skips items already in downloads (any status except failed with
// wl_retry_count < MaxWLRetries — those get retried). Returns (true, nil) when
// queued, (false, nil) when skipped, (false, err) on error.
func enqueueIfNew(ctx context.Context, d PollDeps, userID, quality string, item PlaylistItem) (bool, error) {
	var existingID string
	var retryCount int
	err := d.DB.QueryRowContext(ctx, `
		SELECT id, wl_retry_count FROM downloads
		WHERE user_id=? AND youtube_id=?
		ORDER BY created_at DESC LIMIT 1`,
		userID, item.VideoID,
	).Scan(&existingID, &retryCount)

	if err == nil {
		// Row exists. Skip unless it's a failed WL item that hasn't hit the cap.
		if retryCount >= MaxWLRetries {
			return false, nil // exhausted — leave in WL, UI shows alert
		}
		// Check status: only retry if failed.
		var status string
		row := d.DB.QueryRowContext(ctx, `SELECT status FROM downloads WHERE id=?`, existingID)
		if err := row.Scan(&status); err != nil {
			return false, fmt.Errorf("check status for %s: %w", existingID, err)
		}
		if status != "failed" {
			return false, nil // queued/downloading/done/deleted — skip
		}
		// It's failed and retryable: fall through to re-enqueue under a new ID.
	} else if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}

	id := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := d.DB.ExecContext(ctx, `
		INSERT INTO downloads
		  (id, user_id, youtube_id, title, status, quality, sponsorblock,
		   source, playlist_item_id, wl_retry_count, created_at, updated_at)
		SELECT ?, ?, ?, ?, 'queued', ?, sponsorblock, 'watch_later', ?, ?, ?, ?
		FROM users WHERE id=?`,
		id, userID, item.VideoID, item.Title, quality,
		item.ID, retryCount, now, now, userID,
	); err != nil {
		return false, err
	}

	if !d.Queue.Enqueue(queue.Job{
		ID:             id,
		UserID:         userID,
		YoutubeID:      item.VideoID,
		URL:            item.VideoURL,
		Quality:        quality,
		PlaylistItemID: item.ID,
	}) {
		// Queue rejected — mark failed immediately.
		_, _ = d.DB.ExecContext(ctx,
			`UPDATE downloads SET status='failed', updated_at=? WHERE id=?`,
			time.Now().UTC().Format(time.RFC3339), id,
		)
		return false, nil
	}
	return true, nil
}
