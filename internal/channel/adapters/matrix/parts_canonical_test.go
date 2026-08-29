package matrix

import (
	"testing"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/channel/partsfixture"
)

func TestCanonicalPartsRendering(t *testing.T) {
	t.Parallel()

	msg := channel.Message{Parts: partsfixture.Canonical()}
	formatted := formatMatrixMessage(msg)
	if formatted.Body != partsfixture.CanonicalPlain {
		t.Errorf("formatMatrixMessage(Canonical).Body\n  got:  %q\n  want: %q", formatted.Body, partsfixture.CanonicalPlain)
	}
	if formatted.FormattedBody != partsfixture.CanonicalMatrixHTML {
		t.Errorf("formatMatrixMessage(Canonical).FormattedBody\n  got:  %q\n  want: %q", formatted.FormattedBody, partsfixture.CanonicalMatrixHTML)
	}
	if !formatted.HasHTML {
		t.Errorf("expected HasHTML=true")
	}
}
