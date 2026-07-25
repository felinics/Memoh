package slack

import (
	"testing"

	"github.com/memohai/memoh/domains/channel/gateway"
	"github.com/memohai/memoh/domains/channel/internal/test/fixture"
)

// TestCanonicalPartsRendering pins the Slack adapter's mrkdwn output for
// the shared canonical fixture. Slack's dialect differs from GFM (single-
// asterisk bold, underscore italic, <url|text> links, no fence language),
// so the expected output is fixture.CanonicalSlackMrkdwn.
func TestCanonicalPartsRendering(t *testing.T) {
	t.Parallel()
	msg := gateway.Message{Parts: fixture.Canonical()}
	got := renderSlackMessagePartsMrkdwn(msg)
	if got != fixture.CanonicalSlackMrkdwn {
		t.Errorf("renderSlackMessagePartsMrkdwn(Canonical)\n  got:  %q\n  want: %q", got, fixture.CanonicalSlackMrkdwn)
	}
}
