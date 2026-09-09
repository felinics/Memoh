package slack

import (
	"testing"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/channel/partsfixture"
)

// TestCanonicalPartsRendering pins the Slack adapter's mrkdwn output for
// the shared canonical fixture. Slack's dialect differs from GFM (single-
// asterisk bold, underscore italic, <url|text> links, no fence language),
// so the expected output is partsfixture.CanonicalSlackMrkdwn.
func TestCanonicalPartsRendering(t *testing.T) {
	t.Parallel()
	msg := channel.Message{Parts: partsfixture.Canonical()}
	got := renderSlackMessagePartsMrkdwn(msg)
	if got != partsfixture.CanonicalSlackMrkdwn {
		t.Errorf("renderSlackMessagePartsMrkdwn(Canonical)\n  got:  %q\n  want: %q", got, partsfixture.CanonicalSlackMrkdwn)
	}
}
