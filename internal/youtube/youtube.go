// Package youtube implements the YouTube Data API v3 client, Google OAuth
// (Authorization Code + PKCE), and the Watch Later cron poller.
package youtube

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var videoIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

// ExtractVideoID parses a YouTube URL and returns the 11-character video ID.
// Supports youtube.com/watch?v=, youtu.be/, /shorts/, and /embed/ formats.
func ExtractVideoID(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("URL must use http or https scheme")
	}

	var id string
	switch u.Host {
	case "www.youtube.com", "youtube.com", "m.youtube.com":
		switch {
		case strings.HasPrefix(u.Path, "/watch"):
			id = u.Query().Get("v")
		case strings.HasPrefix(u.Path, "/shorts/"):
			id = firstPathSegment(strings.TrimPrefix(u.Path, "/shorts/"))
		case strings.HasPrefix(u.Path, "/embed/"):
			id = firstPathSegment(strings.TrimPrefix(u.Path, "/embed/"))
		default:
			return "", fmt.Errorf("unrecognised youtube.com path %q", u.Path)
		}
	case "youtu.be":
		id = firstPathSegment(strings.TrimPrefix(u.Path, "/"))
	default:
		return "", fmt.Errorf("unsupported YouTube URL host %q", u.Host)
	}

	if !videoIDPattern.MatchString(id) {
		return "", fmt.Errorf("could not extract a valid video ID from URL %q", rawURL)
	}
	return id, nil
}

// firstPathSegment returns the portion of s before the first '/'.
func firstPathSegment(s string) string {
	seg, _, _ := strings.Cut(s, "/")
	return seg
}
