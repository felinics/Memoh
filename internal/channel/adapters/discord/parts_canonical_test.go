package discord

import (
	"testing"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/channel/partsfixture"
)

// TestCanonicalPartsRendering pins the Discord adapter's GFM output for the
// shared canonical fixture. The expected string lives in the partsfixture
// package, so changing the fixture forces a deliberate update of every
// adapter's regression test in one place.
func TestCanonicalPartsRendering(t *testing.T) {
	t.Parallel()
	msg := channel.Message{Parts: partsfixture.Canonical()}
	got := renderDiscordMessagePartsMarkdown(msg)
	if got != partsfixture.CanonicalMarkdown {
		t.Errorf("renderDiscordMessagePartsMarkdown(Canonical)\n  got:  %q\n  want: %q", got, partsfixture.CanonicalMarkdown)
	}
}
