package nfo_test

import (
	"bytes"
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kondanta/miru/internal/nfo"
)

const sampleInfoJSON = `{
  "id": "dQw4w9WgXcQ",
  "title": "Never Gonna Give You Up",
  "description": "Official music video",
  "channel": "RickAstleyVEVO",
  "upload_date": "20091025",
  "duration": 212,
  "thumbnail": "https://example.com/thumb.jpg",
  "tags": ["rick astley", "pop", "music", "80s", "classic", "vevo"],
  "categories": ["Music"],
  "webpage_url": "https://www.youtube.com/watch?v=dQw4w9WgXcQ"
}`

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid", sampleInfoJSON, false},
		{"missing id", `{"title":"foo"}`, true},
		{"invalid json", `{not json}`, true},
		{"empty", `{}`, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, err := nfo.Parse(strings.NewReader(tt.input))
			if (err != nil) != tt.wantErr {
				t.Fatalf("Parse() err = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if info.ID != "dQw4w9WgXcQ" {
				t.Errorf("ID = %q, want dQw4w9WgXcQ", info.ID)
			}
			if info.Title != "Never Gonna Give You Up" {
				t.Errorf("Title = %q", info.Title)
			}
			if len(info.Tags) != 6 {
				t.Errorf("Tags len = %d, want 6", len(info.Tags))
			}
		})
	}
}

func TestWriteNFO(t *testing.T) {
	type xmlMovie struct {
		XMLName  xml.Name `xml:"movie"`
		Title    string   `xml:"title"`
		Plot     string   `xml:"plot"`
		Year     int      `xml:"year"`
		Runtime  int      `xml:"runtime"`
		Studio   string   `xml:"studio"`
		Tags     []string `xml:"tag"`
		Genres   []string `xml:"genre"`
		UniqueID struct {
			Type  string `xml:"type,attr"`
			Value string `xml:",chardata"`
		} `xml:"uniqueid"`
		Thumb struct {
			Aspect string `xml:"aspect,attr"`
			URL    string `xml:",chardata"`
		} `xml:"thumb"`
	}

	tests := []struct {
		name     string
		json     string
		maxTags  int
		wantYear int
		wantMins int
		wantTags int
	}{
		{
			name:     "standard video",
			json:     sampleInfoJSON,
			maxTags:  3,
			wantYear: 2009,
			wantMins: 4, // ceil(212/60)
			wantTags: 3,
		},
		{
			name:     "zero maxTags writes all",
			json:     sampleInfoJSON,
			maxTags:  0,
			wantYear: 2009,
			wantMins: 4,
			wantTags: 6,
		},
		{
			name: "missing upload_date",
			json: `{"id":"abc","title":"t","duration":90,"channel":"c",
				"tags":[],"categories":[],"thumbnail":"https://x.com/img.jpg"}`,
			maxTags:  10,
			wantYear: 0,
			wantMins: 2, // ceil(90/60)
			wantTags: 0,
		},
		{
			name: "zero duration",
			json: `{"id":"abc","title":"t","upload_date":"20240101","channel":"c",
				"tags":[],"categories":[],"thumbnail":""}`,
			maxTags:  10,
			wantYear: 2024,
			wantMins: 0,
			wantTags: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, err := nfo.Parse(strings.NewReader(tt.json))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}

			var buf bytes.Buffer
			if err := nfo.WriteNFO(&buf, info, tt.maxTags); err != nil {
				t.Fatalf("WriteNFO: %v", err)
			}

			var m xmlMovie
			if err := xml.NewDecoder(&buf).Decode(&m); err != nil {
				t.Fatalf("decode NFO XML: %v\nXML:\n%s", err, buf.String())
			}

			if m.Title != info.Title {
				t.Errorf("title: got %q, want %q", m.Title, info.Title)
			}
			if m.Year != tt.wantYear {
				t.Errorf("year: got %d, want %d", m.Year, tt.wantYear)
			}
			if m.Runtime != tt.wantMins {
				t.Errorf("runtime: got %d, want %d", m.Runtime, tt.wantMins)
			}
			if len(m.Tags) != tt.wantTags {
				t.Errorf("tags: got %d, want %d", len(m.Tags), tt.wantTags)
			}
			if m.UniqueID.Type != "youtube" {
				t.Errorf("uniqueid type: got %q, want youtube", m.UniqueID.Type)
			}
			if m.UniqueID.Value != info.ID {
				t.Errorf("uniqueid value: got %q, want %q", m.UniqueID.Value, info.ID)
			}
		})
	}
}

func TestWriteNFO_NoThumb(t *testing.T) {
	info, err := nfo.Parse(strings.NewReader(
		`{"id":"abc","title":"t","thumbnail":"","tags":[],"categories":[]}`,
	))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	var buf bytes.Buffer
	if err := nfo.WriteNFO(&buf, info, 10); err != nil {
		t.Fatalf("WriteNFO: %v", err)
	}

	if strings.Contains(buf.String(), "<thumb>") {
		t.Error("expected no <thumb> element when thumbnail is empty")
	}
}

func TestFetchPoster(t *testing.T) {
	fakeImage := bytes.Repeat([]byte("jpeg"), 4)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(fakeImage)
		case "/not-found":
			w.WriteHeader(http.StatusNotFound)
		case "/large":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(bytes.Repeat([]byte("x"), 20))
		}
	}))
	defer srv.Close()

	t.Run("success", func(t *testing.T) {
		info := &nfo.Info{Thumbnail: srv.URL + "/ok"}
		var buf bytes.Buffer
		if err := nfo.FetchPoster(context.Background(), &buf, info, srv.Client(), 1024); err != nil {
			t.Fatalf("FetchPoster: %v", err)
		}
		if !bytes.Equal(buf.Bytes(), fakeImage) {
			t.Errorf("got %q, want %q", buf.Bytes(), fakeImage)
		}
	})

	t.Run("non-200", func(t *testing.T) {
		info := &nfo.Info{Thumbnail: srv.URL + "/not-found"}
		var buf bytes.Buffer
		err := nfo.FetchPoster(context.Background(), &buf, info, srv.Client(), 1024)
		if err == nil {
			t.Fatal("expected error for non-200 response")
		}
		if buf.Len() != 0 {
			t.Error("expected nothing written to w on error")
		}
	})

	t.Run("exceeds limit", func(t *testing.T) {
		info := &nfo.Info{Thumbnail: srv.URL + "/large"}
		var buf bytes.Buffer
		err := nfo.FetchPoster(context.Background(), &buf, info, srv.Client(), 10)
		if err == nil {
			t.Fatal("expected error when response exceeds maxBytes")
		}
		if buf.Len() != 0 {
			t.Error("expected nothing written to w when limit exceeded")
		}
	})

	t.Run("no thumbnail URL", func(t *testing.T) {
		info := &nfo.Info{}
		err := nfo.FetchPoster(context.Background(), &bytes.Buffer{}, info, nil, 1024)
		if err == nil {
			t.Fatal("expected error for missing thumbnail URL")
		}
	})
}
