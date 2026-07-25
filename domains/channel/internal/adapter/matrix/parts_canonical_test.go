package matrix

import (
	"testing"

	"github.com/memohai/memoh/domains/channel/gateway"
	"github.com/memohai/memoh/domains/channel/internal/test/fixture"
)

func TestCanonicalPartsRendering(t *testing.T) {
	t.Parallel()

	msg := gateway.Message{Parts: fixture.Canonical()}
	formatted := formatMatrixMessage(msg)
	if formatted.Body != fixture.CanonicalPlain {
		t.Errorf("formatMatrixMessage(Canonical).Body\n  got:  %q\n  want: %q", formatted.Body, fixture.CanonicalPlain)
	}
	if formatted.FormattedBody != fixture.CanonicalMatrixHTML {
		t.Errorf("formatMatrixMessage(Canonical).FormattedBody\n  got:  %q\n  want: %q", formatted.FormattedBody, fixture.CanonicalMatrixHTML)
	}
	if !formatted.HasHTML {
		t.Errorf("expected HasHTML=true")
	}
}
