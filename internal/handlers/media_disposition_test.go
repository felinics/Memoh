package handlers

import "testing"

func TestOnlyPassiveMediaRendersInline(t *testing.T) {
	for contentType, want := range map[string]bool{
		"image/png": true, "image/jpeg": true, "video/mp4": true, "audio/mpeg": true, "application/pdf": true,
		"text/plain; charset=utf-8": true,
		"text/html":                 false, "image/svg+xml": false, "application/xhtml+xml": false, "text/javascript": false,
		"application/xml": false, "text/markdown": false, "application/octet-stream": false, "": false,
	} {
		if got := inlineSafeMedia(contentType); got != want {
			t.Errorf("inlineSafeMedia(%q) = %v, want %v", contentType, got, want)
		}
	}
}
