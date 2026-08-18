package youtube_test

import (
	"testing"

	"github.com/kondanta/miru/internal/youtube"
)

func TestExtractVideoID(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		want    string
		wantErr bool
	}{
		{"watch url", "https://www.youtube.com/watch?v=dQw4w9WgXcQ", "dQw4w9WgXcQ", false},
		{"watch url no www", "https://youtube.com/watch?v=dQw4w9WgXcQ", "dQw4w9WgXcQ", false},
		{"mobile url", "https://m.youtube.com/watch?v=dQw4w9WgXcQ", "dQw4w9WgXcQ", false},
		{"short url", "https://youtu.be/dQw4w9WgXcQ", "dQw4w9WgXcQ", false},
		{"shorts url", "https://www.youtube.com/shorts/dQw4w9WgXcQ", "dQw4w9WgXcQ", false},
		{"embed url", "https://www.youtube.com/embed/dQw4w9WgXcQ", "dQw4w9WgXcQ", false},
		{"short url with query", "https://youtu.be/dQw4w9WgXcQ?t=42", "dQw4w9WgXcQ", false},
		{"missing v param", "https://www.youtube.com/watch", "", true},
		{"bad host", "https://vimeo.com/123456", "", true},
		{"no scheme", "youtube.com/watch?v=dQw4w9WgXcQ", "", true},
		{"ftp scheme", "ftp://youtube.com/watch?v=dQw4w9WgXcQ", "", true},
		{"invalid id length", "https://www.youtube.com/watch?v=short", "", true},
		{"invalid id chars", "https://www.youtube.com/watch?v=dQw4w9WgX!Q", "", true},
		{"watchlist path", "https://www.youtube.com/watchlist?v=dQw4w9WgXcQ", "", true},
		{"empty string", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := youtube.ExtractVideoID(tt.url)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ExtractVideoID(%q) err = %v, wantErr = %v", tt.url, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("ExtractVideoID(%q) = %q, want %q", tt.url, got, tt.want)
			}
		})
	}
}
