package youtube

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

const (
	apiBase        = "https://www.googleapis.com/youtube/v3"
	maxPageResults = 50
)

// PlaylistItem is a single item from the Watch Later playlist.
type PlaylistItem struct {
	// ID is the playlistItems resource ID — used to delete the item.
	ID       string
	VideoID  string
	Title    string
	VideoURL string
}

// ListWatchLater returns all items in the given YouTube playlist.
// playlistID is the ID from the playlist URL (e.g. "PLxxxxxx").
// The native Watch Later playlist ("WL") is not accessible via the API
// — users must create a regular playlist and configure its ID in miru.
// Paginates automatically.
func ListWatchLater(ctx context.Context, client *http.Client, playlistID string) ([]PlaylistItem, error) {
	var items []PlaylistItem
	pageToken := ""
	for {
		q := url.Values{}
		q.Set("part", "snippet")
		q.Set("playlistId", playlistID)
		q.Set("maxResults", fmt.Sprint(maxPageResults))
		if pageToken != "" {
			q.Set("pageToken", pageToken)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			apiBase+"/playlistItems?"+q.Encode(), nil,
		)
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("playlistItems.list: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("playlistItems.list: HTTP %d", resp.StatusCode)
		}
		var page struct {
			NextPageToken string `json:"nextPageToken"`
			Items         []struct {
				ID      string `json:"id"`
				Snippet struct {
					Title      string `json:"title"`
					ResourceID struct {
						VideoID string `json:"videoId"`
					} `json:"resourceId"`
				} `json:"snippet"`
			} `json:"items"`
		}
		err = json.NewDecoder(resp.Body).Decode(&page)
		_ = resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("decode response: %w", err)
		}

		for _, it := range page.Items {
			items = append(items, PlaylistItem{
				ID:       it.ID,
				VideoID:  it.Snippet.ResourceID.VideoID,
				Title:    it.Snippet.Title,
				VideoURL: "https://www.youtube.com/watch?v=" + it.Snippet.ResourceID.VideoID,
			})
		}

		if page.NextPageToken == "" {
			break
		}
		pageToken = page.NextPageToken
	}
	return items, nil
}

// DeletePlaylistItem removes a single item from the Watch Later playlist by its
// playlistItems resource ID.
func DeletePlaylistItem(ctx context.Context, client *http.Client, itemID string) error {
	q := url.Values{}
	q.Set("id", itemID)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		apiBase+"/playlistItems?"+q.Encode(), nil,
	)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("playlistItems.delete: %w", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("playlistItems.delete: HTTP %d", resp.StatusCode)
	}
	return nil
}
