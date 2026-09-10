package email

import (
	"strings"
	"testing"
)

func TestEmailMarkdownMediaFallbackDoesNotLeakPaths(t *testing.T) {
	msg, partial := prepareMarkdownBody(OutboundEmail{Body: "before [report][file] after\n\n[file]: /data/report.pdf"})
	if !partial || strings.Contains(msg.Body, "/data/") || !strings.Contains(msg.Body, "before report") || !strings.Contains(msg.Body, "after") {
		t.Fatalf("msg=%+v partial=%v", msg, partial)
	}
	plain := OutboundEmail{Body: "`[example](/data/example.txt)`"}
	got, partial := prepareMarkdownBody(plain)
	if partial || got.Body != plain.Body {
		t.Fatalf("code changed: %+v", got)
	}
}
