package attachment

import (
	"io"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/media"
)

func TestMapMediaType(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want media.MediaType
	}{
		{name: "image", in: "image", want: media.MediaTypeImage},
		{name: "gif", in: "gif", want: media.MediaTypeImage},
		{name: "audio", in: "audio", want: media.MediaTypeAudio},
		{name: "voice", in: "voice", want: media.MediaTypeAudio},
		{name: "video", in: "video", want: media.MediaTypeVideo},
		{name: "default", in: "file", want: media.MediaTypeFile},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := MapMediaType(tc.in)
			if got != tc.want {
				t.Fatalf("MapMediaType(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizeBase64DataURL(t *testing.T) {
	got := NormalizeBase64DataURL("AAAA", "image/png")
	if got != "data:image/png;base64,AAAA" {
		t.Fatalf("unexpected normalized value: %q", got)
	}

	already := "data:image/jpeg;base64,BBBB"
	if NormalizeBase64DataURL(already, "image/png") != already {
		t.Fatalf("expected data url to pass through")
	}
}

func TestNormalizeMime(t *testing.T) {
	got := NormalizeMime("IMAGE/JPEG; charset=utf-8")
	if got != "image/jpeg" {
		t.Fatalf("NormalizeMime unexpected result: %q", got)
	}
	if got := NormalizeMime("file"); got != "" {
		t.Fatalf("NormalizeMime should drop invalid mime token, got %q", got)
	}
}

func TestMimeFromDataURL(t *testing.T) {
	got := MimeFromDataURL("data:image/png;base64,AAAA")
	if got != "image/png" {
		t.Fatalf("MimeFromDataURL unexpected result: %q", got)
	}
	if MimeFromDataURL("https://example.com/demo.png") != "" {
		t.Fatalf("MimeFromDataURL should return empty for non-data-url")
	}
}

func TestResolveMime(t *testing.T) {
	cases := []struct {
		name      string
		mediaType media.MediaType
		source    string
		sniffed   string
		want      string
	}{
		{
			name:      "image: sniffed preferred over generic source",
			mediaType: media.MediaTypeImage,
			source:    "application/octet-stream",
			sniffed:   "image/jpeg",
			want:      "image/jpeg",
		},
		{
			name:      "image: sniffed preferred over wrong platform mime",
			mediaType: media.MediaTypeImage,
			source:    "image/png",
			sniffed:   "image/jpeg",
			want:      "image/jpeg",
		},
		{
			name:      "image: fallback to source when sniff fails",
			mediaType: media.MediaTypeImage,
			source:    "image/png",
			sniffed:   "",
			want:      "image/png",
		},
		{
			name:      "image: both empty returns octet-stream",
			mediaType: media.MediaTypeImage,
			source:    "",
			sniffed:   "",
			want:      "application/octet-stream",
		},
		{
			name:      "file: source preferred over sniffed",
			mediaType: media.MediaTypeFile,
			source:    "application/pdf",
			sniffed:   "application/octet-stream",
			want:      "application/pdf",
		},
		{
			name:      "file: sniffed used when source is generic",
			mediaType: media.MediaTypeFile,
			source:    "application/octet-stream",
			sniffed:   "application/pdf",
			want:      "application/pdf",
		},
		{
			name:      "file: sniffed used when source token is invalid",
			mediaType: media.MediaTypeFile,
			source:    "file",
			sniffed:   "text/plain",
			want:      "text/plain",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveMime(tc.mediaType, tc.source, tc.sniffed)
			if got != tc.want {
				t.Fatalf("ResolveMime(%v, %q, %q) = %q, want %q",
					tc.mediaType, tc.source, tc.sniffed, got, tc.want)
			}
		})
	}
}

func TestPrepareReaderAndMime(t *testing.T) {
	reader, mime, err := PrepareReaderAndMime(strings.NewReader("\x89PNG\r\n\x1a\npayload"), media.MediaTypeImage, "")
	if err != nil {
		t.Fatalf("PrepareReaderAndMime returned error: %v", err)
	}
	if mime != "image/png" {
		t.Fatalf("PrepareReaderAndMime mime = %q, want image/png", mime)
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read prepared reader failed: %v", err)
	}
	if !strings.HasPrefix(string(raw), "\x89PNG\r\n\x1a\n") {
		t.Fatalf("prepared reader lost prefix bytes")
	}
}

func TestDecodeBase64(t *testing.T) {
	reader, err := DecodeBase64("aGVsbG8=", 1024)
	if err != nil {
		t.Fatalf("DecodeBase64 returned error: %v", err)
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read decoded bytes failed: %v", err)
	}
	if string(raw) != "hello" {
		t.Fatalf("decoded content = %q, want hello", string(raw))
	}

	reader, err = DecodeBase64("data:text/plain;base64,aGVsbG8=", 1024)
	if err != nil {
		t.Fatalf("DecodeBase64 with data URL returned error: %v", err)
	}
	raw, err = io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read decoded data URL bytes failed: %v", err)
	}
	if string(raw) != "hello" {
		t.Fatalf("decoded data URL content = %q, want hello", string(raw))
	}

	_, err = DecodeBase64("", 1024)
	if err == nil {
		t.Fatalf("expected empty base64 to return error")
	}
}
