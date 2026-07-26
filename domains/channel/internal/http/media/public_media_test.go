package media

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

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
