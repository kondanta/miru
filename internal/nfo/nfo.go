// Package nfo generates Jellyfin-compatible NFO files and poster images from
// yt-dlp's --write-info-json output after a download completes.
package nfo

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// Info holds the subset of yt-dlp info JSON fields used for NFO generation.
type Info struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Channel     string   `json:"channel"`
	UploadDate  string   `json:"upload_date"` // YYYYMMDD
	Duration    int      `json:"duration"`    // seconds
	Thumbnail   string   `json:"thumbnail"`   // best thumbnail URL chosen by yt-dlp
	Tags        []string `json:"tags"`
	Categories  []string `json:"categories"`
	WebpageURL  string   `json:"webpage_url"`
}

// Parse reads a yt-dlp info JSON from r and returns the relevant fields.
func Parse(r io.Reader) (*Info, error) {
	var info Info
	if err := json.NewDecoder(r).Decode(&info); err != nil {
		return nil, fmt.Errorf("nfo: parse: %w", err)
	}
	if info.ID == "" {
		return nil, fmt.Errorf("nfo: parse: missing required field id")
	}
	return &info, nil
}

func (info *Info) year() int {
	if len(info.UploadDate) < 4 {
		return 0
	}
	y, _ := strconv.Atoi(info.UploadDate[:4])
	return y
}

// runtimeMinutes returns duration in whole minutes, rounded up.
func (info *Info) runtimeMinutes() int {
	if info.Duration <= 0 {
		return 0
	}
	return (info.Duration + 59) / 60
}

// movie is the XML structure for a Jellyfin movie.nfo file.
type movie struct {
	XMLName   xml.Name `xml:"movie"`
	Title     string   `xml:"title"`
	Plot      string   `xml:"plot,omitempty"`
	Year      int      `xml:"year,omitempty"`
	Runtime   int      `xml:"runtime,omitempty"`
	UniqueID  uniqueID `xml:"uniqueid"`
	Studio    string   `xml:"studio,omitempty"`
	Tags      []string `xml:"tag"`
	Genres    []string `xml:"genre"`
	Thumb     *thumb   `xml:"thumb,omitempty"`
	DateAdded string   `xml:"dateadded,omitempty"`
}

type uniqueID struct {
	Type    string `xml:"type,attr"`
	Default bool   `xml:"default,attr"`
	Value   string `xml:",chardata"`
}

type thumb struct {
	Aspect string `xml:"aspect,attr,omitempty"`
	URL    string `xml:",chardata"`
}

// WriteNFO writes a Jellyfin-compatible movie.nfo XML to w.
// maxTags caps the number of tags written; 0 writes all tags.
func WriteNFO(w io.Writer, info *Info, maxTags int) error {
	tags := info.Tags
	if maxTags > 0 && len(tags) > maxTags {
		tags = tags[:maxTags]
	}

	m := movie{
		Title:     info.Title,
		Plot:      info.Description,
		Year:      info.year(),
		Runtime:   info.runtimeMinutes(),
		UniqueID:  uniqueID{Type: "youtube", Default: true, Value: info.ID},
		Studio:    info.Channel,
		Tags:      tags,
		Genres:    info.Categories,
		DateAdded: time.Now().UTC().Format(time.DateTime),
	}
	if info.Thumbnail != "" {
		m.Thumb = &thumb{Aspect: "poster", URL: info.Thumbnail}
	}

	if _, err := io.WriteString(w, xml.Header); err != nil {
		return fmt.Errorf("nfo: write: %w", err)
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(m); err != nil {
		return fmt.Errorf("nfo: write: %w", err)
	}
	return enc.Close()
}

// FetchPoster downloads the thumbnail from info.Thumbnail and writes it to w.
// maxBytes caps the response body and must be > 0; the full image is buffered
// before writing so that w receives nothing if the limit is exceeded. client
// may be nil to use http.DefaultClient.
func FetchPoster(ctx context.Context, w io.Writer, info *Info, client *http.Client, maxBytes int64) error {
	if info.Thumbnail == "" {
		return fmt.Errorf("nfo: fetch poster: no thumbnail URL")
	}
	if client == nil {
		client = http.DefaultClient
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, info.Thumbnail, nil)
	if err != nil {
		return fmt.Errorf("nfo: fetch poster: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("nfo: fetch poster: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("nfo: fetch poster: unexpected status %d", resp.StatusCode)
	}

	// Read maxBytes+1 to detect oversized responses before writing to w.
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return fmt.Errorf("nfo: fetch poster: read: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return fmt.Errorf("nfo: fetch poster: response exceeds limit of %d bytes", maxBytes)
	}

	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("nfo: fetch poster: write: %w", err)
	}
	return nil
}
