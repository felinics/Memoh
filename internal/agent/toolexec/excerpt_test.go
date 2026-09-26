package toolexec

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// The excerpt of a model's argument text is cut at a rune boundary: the
// text is model output and may be cut in the middle of a multi-byte
// character otherwise.
func TestInvalidArgumentExcerptKeepsValidUTF8(t *testing.T) {
	t.Parallel()
	text := strings.Repeat("字", invalidArgumentExcerptLimit) // 3 bytes each; the byte limit falls inside a rune
	got := invalidArgumentExcerpt(text)
	if !utf8.ValidString(got) {
		t.Fatalf("excerpt is not valid UTF-8: %q", got[len(got)-8:])
	}
	if !strings.HasSuffix(got, "…") || len(got) > invalidArgumentExcerptLimit+len("…") {
		t.Fatalf("excerpt length = %d, want at most the limit plus the ellipsis", len(got))
	}
	if short := invalidArgumentExcerpt("short"); short != "short" {
		t.Fatalf("short text changed: %q", short)
	}
}
