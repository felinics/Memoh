package channel

import (
	"strings"
	"testing"
)

func TestIsPublicMediaPath(t *testing.T) {
	t.Parallel()

	hash := strings.Repeat("a", 64)
	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "preview", path: "/channels/line/public/media/bot-1/" + hash + "/preview.jpg", want: true},
		{name: "original", path: "/channels/line/public/media/bot-1/" + hash + "/original/image.png", want: true},
		{name: "other channel", path: "/channels/telegram/public/media/bot-1/" + hash + "/preview.jpg", want: true},
		{name: "bad hash", path: "/channels/line/public/media/bot-1/not-a-hash/preview.jpg"},
		{name: "bad route", path: "/channels/line/public/media/bot-1/" + hash + "/metadata"},
		{name: "empty original name", path: "/channels/line/public/media/bot-1/" + hash + "/original/"},
		{name: "path traversal platform", path: "/channels/..%2Fsecret/public/media/bot-1/" + hash + "/preview.jpg"},
		{name: "path traversal bot", path: "/channels/line/public/media/..%2Fsecret/" + hash + "/preview.jpg"},
		{name: "sibling prefix", path: "/channels/line/public/media-extra/bot-1/" + hash + "/preview.jpg"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := IsPublicMediaPath(tt.path); got != tt.want {
				t.Fatalf("IsPublicMediaPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}
