package telegram

import (
	"testing"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/channel/partsfixture"
)

// TestCanonicalPartsRendering pins the Telegram adapter's HTML output for
// the shared canonical fixture. Telegram uses paragraph-wrapped inline
// elements + <pre><code class="language-…"> code blocks, per the Bot API
// rich message spec.
func TestCanonicalPartsRendering(t *testing.T) {
	t.Parallel()
	msg := channel.Message{Parts: partsfixture.Canonical()}
	rich := renderTelegramMessagePartsRichMessage(msg)
	if rich.HTML != partsfixture.CanonicalTelegramRichHTML {
		t.Errorf("renderTelegramMessagePartsRichMessage(Canonical).HTML\n  got:  %q\n  want: %q", rich.HTML, partsfixture.CanonicalTelegramRichHTML)
	}
	if !rich.SkipEntityDetection {
		t.Errorf("expected SkipEntityDetection=true")
	}
}
