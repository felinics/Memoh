package discord

import (
	"testing"

	"github.com/memohai/memoh/domains/channel/gateway"
	"github.com/memohai/memoh/domains/channel/internal/test/fixture"
)

// TestCanonicalPartsRendering pins the Discord adapter's GFM output for the
// shared canonical fixture. The expected string lives in the partsfixture
// package, so changing the fixture forces a deliberate update of every
// adapter's regression test in one place.
func TestCanonicalPartsRendering(t *testing.T) {
	t.Parallel()
	msg := gateway.Message{Parts: fixture.Canonical()}
	got := renderDiscordMessagePartsMarkdown(msg)
	if got != fixture.CanonicalMarkdown {
		t.Errorf("renderDiscordMessagePartsMarkdown(Canonical)\n  got:  %q\n  want: %q", got, fixture.CanonicalMarkdown)
	}
}
