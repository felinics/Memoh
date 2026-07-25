package publicmedia

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestIsPath(t *testing.T) {
	t.Parallel()

	hash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "preview", path: PreviewPath("line", "bot-1", hash), want: true},
		{name: "original", path: OriginalPath("line", "bot-1", hash, "image.png"), want: true},
		{name: "other channel", path: PreviewPath("telegram", "bot-1", hash), want: true},
		{name: "bad hash", path: PathPrefix("line") + "bot-1/" + "not-a-hash" + "/preview.jpg"},
		{name: "bad route", path: PathPrefix("line") + "bot-1/" + hash + "/metadata"},
		{name: "empty original name", path: PathPrefix("line") + "bot-1/" + hash + "/original/"},
		{name: "path traversal platform", path: "/channels/..%2Fsecret/public/media/bot-1/" + hash + "/preview.jpg"},
		{name: "path traversal bot", path: "/channels/line/public/media/..%2Fsecret/" + hash + "/preview.jpg"},
		{name: "sibling prefix", path: "/channels/line/public/media-extra/bot-1/" + hash + "/preview.jpg"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := IsPath(tt.path); got != tt.want {
				t.Fatalf("IsPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestSignerSignsAndValidatesPath(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	path := OriginalPath("line", "bot-1", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "image.png")
	signer := NewSigner("secret", time.Hour)
	signed, ok := signer.SignPath(path, now)
	if !ok {
		t.Fatal("SignPath returned false")
	}
	if !strings.HasPrefix(signed, path+"?") {
		t.Fatalf("signed path = %q", signed)
	}
	parsed, err := url.Parse(signed)
	if err != nil {
		t.Fatalf("parse signed path: %v", err)
	}
	if !signer.Validate(parsed.EscapedPath(), parsed.Query(), now.Add(30*time.Minute)) {
		t.Fatal("signed path should validate before expiry")
	}
	if signer.Validate(parsed.EscapedPath(), parsed.Query(), now.Add(2*time.Hour)) {
		t.Fatal("signed path should not validate after expiry")
	}
}

func TestSignerRejectsTamperedPath(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	path := PreviewPath("line", "bot-1", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	signer := NewSigner("secret", time.Hour)
	signed, ok := signer.SignPath(path, now)
	if !ok {
		t.Fatal("SignPath returned false")
	}
	parsed, err := url.Parse(signed)
	if err != nil {
		t.Fatalf("parse signed path: %v", err)
	}
	tampered := PreviewPath("line", "bot-2", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if signer.Validate(tampered, parsed.Query(), now) {
		t.Fatal("tampered path should not validate")
	}
}
