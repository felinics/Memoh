package telegram

import (
	"testing"

	"github.com/memohai/memoh/domains/channel/gateway"
	"github.com/memohai/memoh/domains/channel/internal/test/fixture"
)

// TestCanonicalPartsRendering pins the Telegram adapter's HTML output for
// the shared canonical fixture. Telegram uses paragraph-wrapped inline
// elements + <pre><code class="language-…"> code blocks, per the Bot API
// rich message spec.
func TestCanonicalPartsRendering(t *testing.T) {
	t.Parallel()
	msg := gateway.Message{Parts: fixture.Canonical()}
	rich := renderTelegramMessagePartsRichMessage(msg)
	if rich.HTML != fixture.CanonicalTelegramRichHTML {
		t.Errorf("renderTelegramMessagePartsRichMessage(Canonical).HTML\n  got:  %q\n  want: %q", rich.HTML, fixture.CanonicalTelegramRichHTML)
	}
	if !rich.SkipEntityDetection {
		t.Errorf("expected SkipEntityDetection=true")
	}
}
